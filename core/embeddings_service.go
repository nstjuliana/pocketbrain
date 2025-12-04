package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
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

// EmbeddingRequest represents a request to generate embeddings for records.
type EmbeddingRequest struct {
	CollectionId string   `json:"collectionId"`
	FieldName    string   `json:"fieldName"`
	RecordIds    []string `json:"recordIds,omitempty"` // If empty, process all records
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
	CollectionId string  `json:"collectionId"`
	FieldName    string  `json:"fieldName"`
	Text         string  `json:"text,omitempty"`   // Text to find similar records for
	RecordId     string  `json:"recordId,omitempty"` // Or use existing record's embedding
	Limit        int     `json:"limit"`
}

// FindSimilarResponse represents the response from finding similar records.
type FindSimilarResponse struct {
	Results []SimilarRecord `json:"results"`
	Debug   *SimilarityDebug `json:"debug,omitempty"`
}

// SimilarityDebug contains debug information for similarity search
type SimilarityDebug struct {
	CollectionId      string   `json:"collectionId"`
	FieldName         string   `json:"fieldName"`
	QueryEmbeddingLen int      `json:"queryEmbeddingLen"`
	StoredEmbeddings  int      `json:"storedEmbeddings"`
	ProcessedCount    int      `json:"processedCount"`
	ErrorCount        int      `json:"errorCount"`
	Errors            []string `json:"errors,omitempty"`
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

// GenerateEmbeddings generates vector embeddings for the specified records' text field.
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

	// Verify the field exists and is embeddable
	field := collection.Fields.GetByName(req.FieldName)
	if field == nil {
		return nil, fmt.Errorf("field '%s' not found in collection", req.FieldName)
	}

	// Check if field is embeddable (supports both text and editor fields)
	if !IsFieldEmbeddable(field) {
		return nil, fmt.Errorf("field '%s' is not a text/editor field or is not marked as embeddable", req.FieldName)
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
		text := record.GetString(req.FieldName)
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
				FieldName:    req.FieldName,
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

	return app.Save(record)
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
				"fieldName": req.FieldName,
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

	// Find all embeddings for this collection/field
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
			"fieldName":    req.FieldName,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch embeddings: %w", err)
	}

	// Debug info
	debug := &SimilarityDebug{
		CollectionId:      collectionId,
		FieldName:         req.FieldName,
		QueryEmbeddingLen: len(queryEmbedding),
		StoredEmbeddings:  len(allEmbeddings),
		ProcessedCount:    0,
		ErrorCount:        0,
	}

	// Calculate similarity scores - initialize as empty slice (not nil) for proper JSON
	results := []SimilarRecord{}
	for _, embRecord := range allEmbeddings {
		recordId := embRecord.GetString("record_id")

		// Skip the query record itself
		if recordId == req.RecordId {
			continue
		}

		embedding, err := getEmbeddingFromRecord(embRecord)
		if err != nil {
			debug.ErrorCount++
			// Capture first few errors for debugging
			if len(debug.Errors) < 3 {
				debug.Errors = append(debug.Errors, fmt.Sprintf("record %s: %v", recordId, err))
			}
			continue
		}

		debug.ProcessedCount++
		similarity := cosineSimilarity(queryEmbedding, embedding)
		results = append(results, SimilarRecord{
			RecordId:   recordId,
			Similarity: similarity,
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

