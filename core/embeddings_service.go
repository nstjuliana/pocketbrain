package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/tools/types"
)

const (
	openAIEmbeddingsURL = "https://api.openai.com/v1/embeddings"

	// MaxTextsPerBatch is the maximum number of texts to send in a single embedding request
	MaxTextsPerBatch = 2048

	// MaxTokensPerBatch is an approximate limit on tokens per batch
	MaxTokensPerBatch = 8000
)

const (
	// EmbeddingCacheMaxMemoryMB is the total memory budget for the cache in megabytes
	// When exceeded, oldest entries are evicted until under budget
	EmbeddingCacheMaxMemoryMB = 500 // 500MB total budget

	// EmbeddingCacheMaxPerEntry limits embeddings per entry (secondary protection)
	// Prevents a single huge collection from consuming the entire budget
	EmbeddingCacheMaxPerEntry = 50000

	// EmbeddingCacheTTL is how long cached embeddings remain valid (sliding window)
	EmbeddingCacheTTL = 10 * time.Minute

	// embeddingMemoryPerRecord is the estimated memory per cached embedding in bytes
	// 1536 floats × 4 bytes + record ID (~20 bytes) + magnitude (4 bytes) + overhead
	embeddingMemoryPerRecord = 6200 // ~6.2KB
)

// embeddingCache stores embeddings in memory for fast similarity search
var embeddingCache = &EmbeddingCache{
	cache:     make(map[string]*cacheEntry),
	accessLog: make([]string, 0, 10),
}

// cacheEntry stores embeddings with metadata for LRU and TTL
type cacheEntry struct {
	embeddings []CachedEmbedding
	memoryMB   float64 // Estimated memory usage in MB
	createdAt  time.Time
	accessedAt time.Time
}

// EmbeddingCache provides in-memory caching for embeddings with memory-based eviction and TTL
type EmbeddingCache struct {
	mu            sync.RWMutex
	cache         map[string]*cacheEntry // key: "collectionId:fieldName"
	accessLog     []string               // Track access order for LRU eviction
	totalMemoryMB float64                // Track total memory usage
}

// CachedEmbedding stores a pre-loaded embedding with its record ID
type CachedEmbedding struct {
	RecordId  string
	Embedding []float32
	Magnitude float32 // Pre-computed for faster cosine similarity
}

// cacheKey generates a cache key from collection ID and field name
func cacheKey(collectionId, fieldName string) string {
	return collectionId + ":" + fieldName
}

// Get retrieves cached embeddings for a collection/field
func (c *EmbeddingCache) Get(collectionId, fieldName string) ([]CachedEmbedding, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(collectionId, fieldName)
	entry, ok := c.cache[key]
	if !ok {
		return nil, false
	}

	// Check TTL expiration (sliding window - resets on each access)
	if time.Since(entry.accessedAt) > EmbeddingCacheTTL {
		delete(c.cache, key)
		c.removeFromAccessLog(key)
		return nil, false
	}

	// Update access time for LRU and sliding TTL
	entry.accessedAt = time.Now()
	c.moveToEndOfAccessLog(key)

	return entry.embeddings, true
}

// Set stores embeddings in the cache with memory-based eviction
// Returns true if cached, false if skipped (too large for single entry)
func (c *EmbeddingCache) Set(collectionId, fieldName string, embeddings []CachedEmbedding) bool {
	// Skip caching if single entry exceeds per-entry limit
	if len(embeddings) > EmbeddingCacheMaxPerEntry {
		return false
	}

	// Calculate memory for this entry
	entryMemoryMB := float64(len(embeddings)*embeddingMemoryPerRecord) / (1024 * 1024)

	// Skip if single entry would exceed entire budget
	if entryMemoryMB > EmbeddingCacheMaxMemoryMB {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(collectionId, fieldName)

	// If updating existing entry, subtract its old memory first
	if existing, ok := c.cache[key]; ok {
		c.totalMemoryMB -= existing.memoryMB
	}

	// Evict oldest entries until we have room for the new entry
	for c.totalMemoryMB+entryMemoryMB > EmbeddingCacheMaxMemoryMB && len(c.accessLog) > 0 {
		oldestKey := c.accessLog[0]
		if oldEntry, ok := c.cache[oldestKey]; ok {
			c.totalMemoryMB -= oldEntry.memoryMB
			delete(c.cache, oldestKey)
		}
		c.accessLog = c.accessLog[1:]
	}

	now := time.Now()
	c.cache[key] = &cacheEntry{
		embeddings: embeddings,
		memoryMB:   entryMemoryMB,
		createdAt:  now,
		accessedAt: now,
	}
	c.totalMemoryMB += entryMemoryMB

	// Add to access log if not already present
	c.removeFromAccessLog(key)
	c.accessLog = append(c.accessLog, key)
	return true
}

// Invalidate removes cached embeddings for a collection/field
func (c *EmbeddingCache) Invalidate(collectionId, fieldName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cacheKey(collectionId, fieldName)
	if entry, ok := c.cache[key]; ok {
		c.totalMemoryMB -= entry.memoryMB
		delete(c.cache, key)
	}
	c.removeFromAccessLog(key)
}

// InvalidateCollection removes all cached embeddings for a collection
func (c *EmbeddingCache) InvalidateCollection(collectionId string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := collectionId + ":"
	for key, entry := range c.cache {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.totalMemoryMB -= entry.memoryMB
			delete(c.cache, key)
			c.removeFromAccessLog(key)
		}
	}
}

// removeFromAccessLog removes a key from the access log (helper, must hold lock)
func (c *EmbeddingCache) removeFromAccessLog(key string) {
	for i, k := range c.accessLog {
		if k == key {
			c.accessLog = append(c.accessLog[:i], c.accessLog[i+1:]...)
			return
		}
	}
}

// moveToEndOfAccessLog moves a key to the end of access log (most recently used)
func (c *EmbeddingCache) moveToEndOfAccessLog(key string) {
	c.removeFromAccessLog(key)
	c.accessLog = append(c.accessLog, key)
}

// Stats returns cache statistics for monitoring
func (c *EmbeddingCache) Stats() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	totalEmbeddings := 0
	entries := make([]map[string]any, 0, len(c.cache))

	for key, entry := range c.cache {
		count := len(entry.embeddings)
		totalEmbeddings += count

		entries = append(entries, map[string]any{
			"key":        key,
			"count":      count,
			"memoryMB":   entry.memoryMB,
			"age":        time.Since(entry.createdAt).String(),
			"lastAccess": time.Since(entry.accessedAt).String(),
		})
	}

	return map[string]any{
		"entriesCount":     len(c.cache),
		"totalEmbeddings":  totalEmbeddings,
		"memoryUsedMB":     c.totalMemoryMB,
		"memoryBudgetMB":   EmbeddingCacheMaxMemoryMB,
		"memoryUsagePercent": (c.totalMemoryMB / EmbeddingCacheMaxMemoryMB) * 100,
		"maxPerEntry":      EmbeddingCacheMaxPerEntry,
		"ttl":              EmbeddingCacheTTL.String(),
		"entries":          entries,
	}
}

// Clear removes all cached embeddings
func (c *EmbeddingCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*cacheEntry)
	c.accessLog = make([]string, 0, 10)
	c.totalMemoryMB = 0
}

// Info returns a summary of cache state
func (c *EmbeddingCache) Info() *CacheInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &CacheInfo{
		EntriesCount:       len(c.cache),
		MemoryUsedMB:       c.totalMemoryMB,
		MemoryBudgetMB:     EmbeddingCacheMaxMemoryMB,
		MemoryUsagePercent: (c.totalMemoryMB / EmbeddingCacheMaxMemoryMB) * 100,
	}
}

// computeMagnitude calculates the magnitude (L2 norm) of a vector
func computeMagnitude(v []float32) float32 {
	var sum float32
	for _, val := range v {
		sum += val * val
	}
	return float32(math.Sqrt(float64(sum)))
}

// GetEmbeddingCacheStats returns statistics about the embedding cache
func GetEmbeddingCacheStats() map[string]any {
	return embeddingCache.Stats()
}

// ClearEmbeddingCache clears all cached embeddings
func ClearEmbeddingCache() {
	embeddingCache.Clear()
}

const (
	// RecordLevelFieldName is the special field name used for record-level embeddings
	RecordLevelFieldName = "_record"
)

// EmbeddingMode represents the mode for embedding generation
type EmbeddingMode string

const (
	EmbeddingModeField  EmbeddingMode = "field"  // Embed individual fields
	EmbeddingModeRecord EmbeddingMode = "record" // Embed entire record as one text
)

// EmbeddingRequest represents a request to generate embeddings for records.
type EmbeddingRequest struct {
	CollectionId string        `json:"collectionId"`
	FieldName    string        `json:"fieldName,omitempty"`              // For field-level mode
	Mode         EmbeddingMode `json:"mode,omitempty"`                   // "field" or "record"
	RecordIds    []string      `json:"recordIds,omitempty"`              // If empty, process all records
	Template     string        `json:"template,omitempty"`               // Optional template for record-level mode
}

// EmbeddingResponse represents the response from embedding generation.
type EmbeddingResponse struct {
	Generated int      `json:"generated"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

// SimilarRecord represents a record with its similarity score.
type SimilarRecord struct {
	RecordId   string  `json:"recordId"`
	Similarity float32 `json:"similarity"`
}

// FindSimilarRequest represents a request to find similar records.
type FindSimilarRequest struct {
	CollectionId string        `json:"collectionId"`
	FieldName    string        `json:"fieldName,omitempty"` // For field-level search
	Mode         EmbeddingMode `json:"mode,omitempty"`      // "field" or "record"
	Text         string        `json:"text,omitempty"`      // Text to find similar records for
	RecordId     string        `json:"recordId,omitempty"`  // Or use existing record's embedding
	Limit        int           `json:"limit"`
}

// FindSimilarResponse represents the response from finding similar records.
type FindSimilarResponse struct {
	Results []SimilarRecord `json:"results"`
	Debug   *SimilarityDebug `json:"debug,omitempty"`
}

// SimilarityDebug contains debug information for similarity search
type SimilarityDebug struct {
	CollectionId      string     `json:"collectionId"`
	FieldName         string     `json:"fieldName"`
	QueryEmbeddingLen int        `json:"queryEmbeddingLen"`
	StoredEmbeddings  int        `json:"storedEmbeddings"`
	ProcessedCount    int        `json:"processedCount"`
	ErrorCount        int        `json:"errorCount"`
	CacheHit          bool       `json:"cacheHit"`
	CacheSkipped      bool       `json:"cacheSkipped,omitempty"` // True if too large to cache
	CacheStats        *CacheInfo `json:"cacheStats,omitempty"`
	Errors            []string   `json:"errors,omitempty"`
}

// CacheInfo contains summary info about the embedding cache
type CacheInfo struct {
	EntriesCount       int     `json:"entriesCount"`
	MemoryUsedMB       float64 `json:"memoryUsedMB"`
	MemoryBudgetMB     float64 `json:"memoryBudgetMB"`
	MemoryUsagePercent float64 `json:"memoryUsagePercent"`
}

// OpenAI Embeddings API structures
type openAIEmbeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format,omitempty"`
	Dimensions     int      `json:"dimensions,omitempty"`
}

type openAIEmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// GenerateRecordText creates a text representation of an entire record for embedding.
// It concatenates all text and editor fields into a structured format.
// If a template is provided, it uses that instead (supports {fieldName} placeholders).
func GenerateRecordText(record *Record, collection *Collection, template string) string {
	if template != "" {
		// Use custom template with {fieldName} placeholders
		result := template
		for _, field := range collection.Fields {
			placeholder := "{" + field.GetName() + "}"
			value := record.GetString(field.GetName())
			// Strip HTML for editor fields
			if field.Type() == "editor" {
				value = stripHTML(value)
			}
			result = strings.ReplaceAll(result, placeholder, value)
		}
		return strings.TrimSpace(result)
	}

	// Default format: structured key-value pairs
	var parts []string
	for _, field := range collection.Fields {
		fieldType := field.Type()
		// Include text, editor, and some other useful fields
		if fieldType == "text" || fieldType == "editor" || fieldType == "email" || fieldType == "url" {
			name := field.GetName()
			value := record.GetString(name)
			if value == "" {
				continue
			}
			// Strip HTML for editor fields
			if fieldType == "editor" {
				value = stripHTML(value)
			}
			// Truncate very long values to avoid token limits
			if len(value) > 2000 {
				value = value[:2000] + "..."
			}
			parts = append(parts, fmt.Sprintf("%s: %s", name, value))
		}
	}
	return strings.Join(parts, "\n")
}

// stripHTML removes HTML tags from a string
func stripHTML(s string) string {
	// Simple HTML stripping - removes tags
	result := s
	for {
		start := strings.Index(result, "<")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], ">")
		if end == -1 {
			break
		}
		result = result[:start] + " " + result[start+end+1:]
	}
	// Clean up multiple spaces
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return strings.TrimSpace(result)
}

// GenerateEmbeddings generates vector embeddings for records.
// Supports two modes:
// - "field" (default): Embed a specific text/editor field
// - "record": Embed the entire record as a single text representation
func GenerateEmbeddings(app App, req EmbeddingRequest) (*EmbeddingResponse, error) {
	settings := app.Settings()

	if !settings.AI.Enabled {
		return nil, fmt.Errorf("AI features are not enabled")
	}

	if settings.AI.APIKey == "" {
		return nil, fmt.Errorf("AI API key is not configured")
	}

	if settings.AI.EmbeddingModel == "" {
		return nil, fmt.Errorf("embedding model is not configured")
	}

	// Find the collection
	collection, err := app.FindCollectionByNameOrId(req.CollectionId)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %w", err)
	}

	// Determine embedding mode (default to field-level for backwards compatibility)
	mode := req.Mode
	if mode == "" {
		mode = EmbeddingModeField
	}

	// For field mode, verify the field exists and is embeddable
	fieldName := req.FieldName
	if mode == EmbeddingModeField {
		if fieldName == "" {
			return nil, fmt.Errorf("fieldName is required for field-level embedding mode")
		}
		field := collection.Fields.GetByName(fieldName)
		if field == nil {
			return nil, fmt.Errorf("field '%s' not found in collection", fieldName)
		}
		if !IsFieldEmbeddable(field) {
			return nil, fmt.Errorf("field '%s' is not a text/editor field or is not marked as embeddable", fieldName)
		}
	} else if mode == EmbeddingModeRecord {
		// For record mode, use special field name
		fieldName = RecordLevelFieldName
	} else {
		return nil, fmt.Errorf("invalid embedding mode: %s (must be 'field' or 'record')", mode)
	}

	// Ensure embeddings collection exists
	embeddingsCollection, err := EnsureEmbeddingsCollection(app)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure embeddings collection: %w", err)
	}

	// Fetch records to process
	var records []*Record
	if len(req.RecordIds) > 0 {
		// Fetch specific records
		for _, id := range req.RecordIds {
			record, err := app.FindRecordById(collection.Id, id)
			if err == nil {
				records = append(records, record)
			}
		}
	} else {
		// Fetch all records
		records, err = app.FindAllRecords(collection.Id)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch records: %w", err)
		}
	}

	if len(records) == 0 {
		return &EmbeddingResponse{Generated: 0, Skipped: 0}, nil
	}

	// Extract text values and record IDs
	type textRecord struct {
		RecordId string
		Text     string
	}
	var textsToEmbed []textRecord

	for _, record := range records {
		var text string
		if mode == EmbeddingModeRecord {
			// Generate full record text representation
			text = GenerateRecordText(record, collection, req.Template)
		} else {
			// Get specific field value
			text = record.GetString(fieldName)
			// Strip HTML for editor fields
			field := collection.Fields.GetByName(fieldName)
			if field != nil && field.Type() == "editor" {
				text = stripHTML(text)
			}
		}

		if text != "" {
			textsToEmbed = append(textsToEmbed, textRecord{
				RecordId: record.Id,
				Text:     text,
			})
		}
	}

	if len(textsToEmbed) == 0 {
		return &EmbeddingResponse{Generated: 0, Skipped: len(records)}, nil
	}

	// Process in batches
	response := &EmbeddingResponse{}
	batches := batchTexts(textsToEmbed)

	for _, batch := range batches {
		// Extract just the texts for the API call
		texts := make([]string, len(batch))
		for i, tr := range batch {
			texts[i] = tr.Text
		}

		// Call OpenAI API
		embeddings, err := callOpenAIEmbeddings(app, texts)
		if err != nil {
			response.Errors = append(response.Errors, fmt.Sprintf("batch error: %s", err.Error()))
			response.Skipped += len(batch)
			continue
		}

		// Store embeddings
		for i, embedding := range embeddings {
			if i >= len(batch) {
				break
			}
			tr := batch[i]

			err := storeEmbedding(app, embeddingsCollection, StoreEmbeddingParams{
				RecordId:     tr.RecordId,
				CollectionId: collection.Id,
				FieldName:    fieldName,
				Embedding:    embedding,
				Model:        settings.AI.EmbeddingModel,
				Dimensions:   len(embedding),
			})
			if err != nil {
				response.Errors = append(response.Errors, fmt.Sprintf("record %s: %s", tr.RecordId, err.Error()))
				response.Skipped++
			} else {
				response.Generated++
			}
		}
	}

	// Limit errors to 10
	if len(response.Errors) > 10 {
		response.Errors = append(response.Errors[:10], fmt.Sprintf("... and %d more errors", len(response.Errors)-10))
	}

	return response, nil
}

// batchTexts groups texts into batches respecting token limits
func batchTexts[T any](texts []T) [][]T {
	if len(texts) == 0 {
		return nil
	}

	// Simple batching by count (can be enhanced with token counting)
	var batches [][]T
	batchSize := MaxTextsPerBatch
	if batchSize > len(texts) {
		batchSize = len(texts)
	}

	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batches = append(batches, texts[i:end])
	}

	return batches
}

// callOpenAIEmbeddings calls the OpenAI embeddings API with a batch of texts
func callOpenAIEmbeddings(app App, texts []string) ([][]float32, error) {
	settings := app.Settings()

	reqBody := openAIEmbeddingRequest{
		Model:          settings.AI.EmbeddingModel,
		Input:          texts,
		EncodingFormat: "float",
	}

	// Add dimensions if using text-embedding-3 models
	if settings.AI.EmbeddingDimensions > 0 {
		reqBody.Dimensions = settings.AI.EmbeddingDimensions
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", openAIEmbeddingsURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", settings.AI.APIKey))

	client := &http.Client{
		Timeout: 120 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to call OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var openAIResp openAIEmbeddingResponse
	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Sort by index to maintain order
	sort.Slice(openAIResp.Data, func(i, j int) bool {
		return openAIResp.Data[i].Index < openAIResp.Data[j].Index
	})

	embeddings := make([][]float32, len(openAIResp.Data))
	for i, data := range openAIResp.Data {
		embeddings[i] = data.Embedding
	}

	return embeddings, nil
}

// StoreEmbeddingParams contains parameters for storing an embedding
type StoreEmbeddingParams struct {
	RecordId     string
	CollectionId string
	FieldName    string
	Embedding    []float32
	Model        string
	Dimensions   int
}

// storeEmbedding stores or updates an embedding in the embeddings collection
func storeEmbedding(app App, embeddingsCollection *Collection, params StoreEmbeddingParams) error {
	// Check if embedding already exists for this record+field
	existingRecords, err := app.FindRecordsByFilter(
		embeddingsCollection.Id,
		"record_id = {:recordId} && field_name = {:fieldName}",
		"",
		1,
		0,
		map[string]any{
			"recordId":  params.RecordId,
			"fieldName": params.FieldName,
		},
	)

	var record *Record
	if err == nil && len(existingRecords) > 0 {
		// Update existing
		record = existingRecords[0]
	} else {
		// Create new
		record = NewRecord(embeddingsCollection)
		record.Set("record_id", params.RecordId)
		record.Set("collection_id", params.CollectionId)
		record.Set("field_name", params.FieldName)
	}

	// Store embedding as JSON array (convert float32 to float64 for JSON compatibility)
	embeddingJSON := make([]float64, len(params.Embedding))
	for i, v := range params.Embedding {
		embeddingJSON[i] = float64(v)
	}

	record.Set("embedding", embeddingJSON)
	record.Set("model", params.Model)
	record.Set("dimensions", params.Dimensions)

	err = app.Save(record)
	if err == nil {
		// Invalidate cache for this collection/field since embeddings changed
		embeddingCache.Invalidate(params.CollectionId, params.FieldName)
	}
	return err
}

// FindSimilarRecords finds records similar to the given text or record
func FindSimilarRecords(app App, req FindSimilarRequest) (*FindSimilarResponse, error) {
	settings := app.Settings()

	if !settings.AI.Enabled {
		return nil, fmt.Errorf("AI features are not enabled")
	}

	// Resolve collection ID (user might pass name or ID)
	collection, err := app.FindCollectionByNameOrId(req.CollectionId)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %w", err)
	}
	collectionId := collection.Id // Use the actual ID for queries

	// Determine field name based on mode
	mode := req.Mode
	if mode == "" {
		mode = EmbeddingModeField
	}

	fieldName := req.FieldName
	if mode == EmbeddingModeRecord {
		fieldName = RecordLevelFieldName
	} else if fieldName == "" {
		return nil, fmt.Errorf("fieldName is required for field-level search mode")
	}

	// Get the query embedding
	var queryEmbedding []float32

	if req.Text != "" {
		// Generate embedding for the query text
		embeddings, err := callOpenAIEmbeddings(app, []string{req.Text})
		if err != nil {
			return nil, fmt.Errorf("failed to generate query embedding: %w", err)
		}
		if len(embeddings) == 0 {
			return nil, fmt.Errorf("no embedding returned for query text")
		}
		queryEmbedding = embeddings[0]
	} else if req.RecordId != "" {
		// Find existing embedding for the record
		embeddingsCollection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
		if err != nil {
			return nil, fmt.Errorf("embeddings collection not found: %w", err)
		}

		records, err := app.FindRecordsByFilter(
			embeddingsCollection.Id,
			"record_id = {:recordId} && field_name = {:fieldName}",
			"",
			1,
			0,
			map[string]any{
				"recordId":  req.RecordId,
				"fieldName": fieldName,
			},
		)
		if err != nil || len(records) == 0 {
			return nil, fmt.Errorf("no embedding found for record %s", req.RecordId)
		}

		queryEmbedding, err = getEmbeddingFromRecord(records[0])
		if err != nil {
			return nil, fmt.Errorf("failed to parse existing embedding: %w", err)
		}
	} else {
		return nil, fmt.Errorf("either text or recordId must be provided")
	}

	// Try to get embeddings from cache first
	cachedEmbeddings, cacheHit := embeddingCache.Get(collectionId, fieldName)

	// Debug info
	debug := &SimilarityDebug{
		CollectionId:      collectionId,
		FieldName:         fieldName,
		QueryEmbeddingLen: len(queryEmbedding),
		ProcessedCount:    0,
		ErrorCount:        0,
		CacheHit:          cacheHit,
	}

	if !cacheHit {
		// Load embeddings from database and cache them
		embeddingsCollection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
		if err != nil {
			return nil, fmt.Errorf("embeddings collection not found: %w", err)
		}

		allEmbeddings, err := app.FindRecordsByFilter(
			embeddingsCollection.Id,
			"collection_id = {:collectionId} && field_name = {:fieldName}",
			"",
			0, // Get all
			0,
			map[string]any{
				"collectionId": collectionId,
				"fieldName":    fieldName,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch embeddings: %w", err)
		}

		debug.StoredEmbeddings = len(allEmbeddings)

		// Parse and cache all embeddings with pre-computed magnitudes
		cachedEmbeddings = make([]CachedEmbedding, 0, len(allEmbeddings))
		for _, embRecord := range allEmbeddings {
			recordId := embRecord.GetString("record_id")
			embedding, err := getEmbeddingFromRecord(embRecord)
			if err != nil {
				debug.ErrorCount++
				if len(debug.Errors) < 3 {
					debug.Errors = append(debug.Errors, fmt.Sprintf("record %s: %v", recordId, err))
				}
				continue
			}
			cachedEmbeddings = append(cachedEmbeddings, CachedEmbedding{
				RecordId:  recordId,
				Embedding: embedding,
				Magnitude: computeMagnitude(embedding),
			})
		}

		// Store in cache for future queries (skipped if too large)
		cached := embeddingCache.Set(collectionId, fieldName, cachedEmbeddings)
		if !cached {
			debug.CacheSkipped = true
		}
	} else {
		debug.StoredEmbeddings = len(cachedEmbeddings)
	}

	// Pre-compute query magnitude for optimized similarity calculation
	queryMagnitude := computeMagnitude(queryEmbedding)

	// Use parallel computation for similarity scores
	numWorkers := runtime.NumCPU()
	if numWorkers > len(cachedEmbeddings) {
		numWorkers = len(cachedEmbeddings)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Channel for results
	type similarityResult struct {
		recordId   string
		similarity float32
	}
	resultsChan := make(chan similarityResult, len(cachedEmbeddings))

	// Split work across goroutines
	var wg sync.WaitGroup
	chunkSize := (len(cachedEmbeddings) + numWorkers - 1) / numWorkers

	for i := 0; i < numWorkers; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(cachedEmbeddings) {
			end = len(cachedEmbeddings)
		}
		if start >= end {
			continue
		}

		wg.Add(1)
		go func(embeddings []CachedEmbedding) {
			defer wg.Done()
			for _, cached := range embeddings {
				// Skip the query record itself
				if cached.RecordId == req.RecordId {
					continue
				}
				// Optimized cosine similarity using pre-computed magnitudes
				similarity := cosineSimilarityOptimized(queryEmbedding, queryMagnitude, cached.Embedding, cached.Magnitude)
				resultsChan <- similarityResult{cached.RecordId, similarity}
			}
		}(cachedEmbeddings[start:end])
	}

	// Close channel when all workers done
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	results := make([]SimilarRecord, 0, len(cachedEmbeddings))
	for result := range resultsChan {
		debug.ProcessedCount++
		results = append(results, SimilarRecord{
			RecordId:   result.recordId,
			Similarity: result.similarity,
		})
	}

	// Sort by similarity (descending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	// Limit results
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > len(results) {
		limit = len(results)
	}
	results = results[:limit]

	// Add cache stats to debug info
	debug.CacheStats = embeddingCache.Info()

	return &FindSimilarResponse{Results: results, Debug: debug}, nil
}

// cosineSimilarity calculates the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// cosineSimilarityOptimized calculates cosine similarity using pre-computed magnitudes
// This avoids recomputing magnitudes for cached embeddings on every query
func cosineSimilarityOptimized(a []float32, magA float32, b []float32, magB float32) float32 {
	if len(a) != len(b) || len(a) == 0 || magA == 0 || magB == 0 {
		return 0
	}

	// Only compute dot product - magnitudes are pre-computed
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}

	return dot / (magA * magB)
}

// getEmbeddingFromRecord extracts the embedding vector from a record's JSON field
func getEmbeddingFromRecord(record *Record) ([]float32, error) {
	raw := record.Get("embedding")
	if raw == nil {
		return nil, fmt.Errorf("embedding field is nil")
	}

	// Handle different possible types from JSON unmarshaling
	switch v := raw.(type) {
	case types.JSONRaw:
		// PocketBase stores JSON fields as types.JSONRaw (raw JSON bytes)
		var floats []float64
		if err := json.Unmarshal(v, &floats); err != nil {
			return nil, fmt.Errorf("failed to unmarshal JSONRaw: %w", err)
		}
		result := make([]float32, len(floats))
		for i, val := range floats {
			result[i] = float32(val)
		}
		return result, nil
	case []any:
		result := make([]float32, len(v))
		for i, val := range v {
			switch n := val.(type) {
			case float64:
				result[i] = float32(n)
			case float32:
				result[i] = n
			case int:
				result[i] = float32(n)
			default:
				return nil, fmt.Errorf("unexpected type in embedding array at index %d: %T", i, val)
			}
		}
		return result, nil
	case []float64:
		result := make([]float32, len(v))
		for i, val := range v {
			result[i] = float32(val)
		}
		return result, nil
	case []float32:
		return v, nil
	case string:
		// JSON field might be stored as string - try to unmarshal
		var floats []float64
		if err := json.Unmarshal([]byte(v), &floats); err != nil {
			return nil, fmt.Errorf("failed to unmarshal embedding string: %w", err)
		}
		result := make([]float32, len(floats))
		for i, val := range floats {
			result[i] = float32(val)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unexpected embedding type: %T, value preview: %v", raw, fmt.Sprintf("%v", raw)[:min(100, len(fmt.Sprintf("%v", raw)))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// EmbeddableField represents a field that can have embeddings generated
type EmbeddableField struct {
	Name string
	Type string
}

// IsFieldEmbeddable checks if a field is embeddable (text or editor with embeddable flag)
func IsFieldEmbeddable(field Field) bool {
	switch f := field.(type) {
	case *TextField:
		return f.Embeddable
	case *EditorField:
		return f.Embeddable
	default:
		return false
	}
}

// GetEmbeddableFields returns all embeddable fields from a collection (text and editor)
func GetEmbeddableFields(collection *Collection) []EmbeddableField {
	var fields []EmbeddableField
	for _, f := range collection.Fields {
		if IsFieldEmbeddable(f) {
			fields = append(fields, EmbeddableField{
				Name: f.GetName(),
				Type: f.Type(),
			})
		}
	}
	return fields
}

