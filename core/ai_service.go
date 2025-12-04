package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brianvoe/gofakeit/v7"
)

const (
	openAIAPIURL = "https://api.openai.com/v1/chat/completions"

	// HybridThreshold is the count above which we switch to hybrid generation
	HybridThreshold = 20

	// ArchetypeCount is the number of AI-generated archetypes for hybrid mode
	ArchetypeCount = 12
)

// =====================================================
// ARCHETYPE CACHE - In-memory cache for AI archetypes
// =====================================================

// CachedArchetypes stores archetypes for a collection with schema validation
type CachedArchetypes struct {
	SchemaHash string
	Archetypes []map[string]any
	Fields     []SeedFieldInfo
	CreatedAt  time.Time
}

// ArchetypeCache manages cached archetypes per collection
type ArchetypeCache struct {
	mu    sync.RWMutex
	cache map[string]*CachedArchetypes
}

// Global archetype cache instance
var globalArchetypeCache = &ArchetypeCache{
	cache: make(map[string]*CachedArchetypes),
}

// Get retrieves cached archetypes if the schema hash matches
func (c *ArchetypeCache) Get(collectionID, schemaHash string) (*CachedArchetypes, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cached, exists := c.cache[collectionID]
	if !exists || cached.SchemaHash != schemaHash {
		return nil, false
	}
	return cached, true
}

// Set stores archetypes in the cache
func (c *ArchetypeCache) Set(collectionID string, cached *CachedArchetypes) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[collectionID] = cached
}

// Invalidate removes cached archetypes for a collection
func (c *ArchetypeCache) Invalidate(collectionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, collectionID)
}

// computeSchemaHash generates a hash of the collection's field schema
// This is used to invalidate cache when schema changes
func computeSchemaHash(fields []SeedFieldInfo) string {
	// Sort fields by name for consistent hashing
	sorted := make([]SeedFieldInfo, len(fields))
	copy(sorted, fields)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	// Create a string representation
	var builder strings.Builder
	for _, f := range sorted {
		builder.WriteString(fmt.Sprintf("%s:%s:%v:%v:%v:%d;",
			f.Name, f.Type, f.Min, f.Max, f.Values, f.MaxSelect))
	}

	// Hash it
	hash := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(hash[:8]) // First 8 bytes is enough
}

// ExistingField represents a simplified field for context.
type ExistingField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// GenerateSchemaRequest represents a request to generate a collection schema.
type GenerateSchemaRequest struct {
	Prompt            string          `json:"prompt"`
	CollectionType    string          `json:"collectionType"` // "base", "auth", or "view"
	CurrentCollection string          `json:"currentCollection,omitempty"`
	ExistingFields    []ExistingField `json:"existingFields,omitempty"`
}

// GenerateSchemaResponse represents the response from schema generation.
type GenerateSchemaResponse struct {
	Collection *Collection `json:"collection"`
	Error      string      `json:"error,omitempty"`
}

// GenerateSchemaFromPrompt uses OpenAI to generate a PocketBase collection schema from natural language.
func GenerateSchemaFromPrompt(app App, req GenerateSchemaRequest) (*Collection, error) {
	settings := app.Settings()
	
	if !settings.AI.Enabled {
		return nil, fmt.Errorf("AI features are not enabled")
	}

	if settings.AI.APIKey == "" {
		return nil, fmt.Errorf("AI API key is not configured")
	}

	if settings.AI.Provider != "openai" {
		return nil, fmt.Errorf("unsupported AI provider: %s", settings.AI.Provider)
	}

	// Build the system prompt with context about PocketBase field types
	systemPrompt := buildSystemPrompt(req.CollectionType)
	
	// Build the user prompt with context about existing collection
	var userPrompt string
	if req.CurrentCollection != "" && len(req.ExistingFields) > 0 {
		// User is editing an existing collection - provide context
		existingFieldsStr := make([]string, len(req.ExistingFields))
		for i, f := range req.ExistingFields {
			existingFieldsStr[i] = fmt.Sprintf("%s (%s)", f.Name, f.Type)
		}
		userPrompt = fmt.Sprintf(
			`I'm editing a collection named '%s' which already has these fields: %s.

User request: %s

CRITICAL INSTRUCTIONS:
1. Keep the collection name as '%s'
2. Return ONLY the NEW field(s) to add - DO NOT include any existing fields
3. If user says "add a X field" or "add X", create a field with name exactly as specified (e.g., "add a metadata field" → name: "metadata")
4. Use the field name the user provides, not a generic name like "id"
5. Choose the most appropriate type based on the field name and context`,
			req.CurrentCollection,
			strings.Join(existingFieldsStr, ", "),
			req.Prompt,
			req.CurrentCollection,
		)
	} else if req.CurrentCollection != "" {
		// New collection with a name already set
		userPrompt = fmt.Sprintf(
			"Create fields for a collection named '%s'. User request: %s\n\nKeep the collection name as '%s'.",
			req.CurrentCollection,
			req.Prompt,
			req.CurrentCollection,
		)
	} else {
		// Creating a brand new collection
		userPrompt = fmt.Sprintf("Create a PocketBase %s collection schema for: %s", req.CollectionType, req.Prompt)
	}

	// Prepare OpenAI API request
	openAIReq := map[string]interface{}{
		"model": settings.AI.Model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": systemPrompt,
			},
			{
				"role":    "user",
				"content": userPrompt,
			},
		},
		"temperature": 0.3,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", openAIAPIURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", settings.AI.APIKey))

	// Make the request
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to call OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read OpenAI response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse OpenAI response
	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	content := openAIResp.Choices[0].Message.Content

	// Parse the collection JSON from the response
	var collectionData map[string]interface{}
	if err := json.Unmarshal([]byte(content), &collectionData); err != nil {
		return nil, fmt.Errorf("failed to parse collection JSON: %w", err)
	}

	// Ensure collection type is set
	if req.CollectionType == "" {
		req.CollectionType = CollectionTypeBase
	}
	collectionData["type"] = req.CollectionType

	// Create collection from JSON
	collectionJSON, err := json.Marshal(collectionData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal collection data: %w", err)
	}

	collection := NewCollection(req.CollectionType, "")
	if err := json.Unmarshal(collectionJSON, collection); err != nil {
		return nil, fmt.Errorf("failed to unmarshal collection: %w", err)
	}

	// Ensure collection has a name
	if collection.Name == "" {
		// Generate a name from the prompt (simple slugify)
		name := strings.ToLower(req.Prompt)
		name = strings.ReplaceAll(name, " ", "_")
		name = strings.ReplaceAll(name, "-", "_")
		// Remove special characters
		var builder strings.Builder
		for _, r := range name {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
				builder.WriteRune(r)
			}
		}
		collection.Name = builder.String()
		if collection.Name == "" {
			collection.Name = "collection"
		}
	}

	return collection, nil
}

// TestAIConnection tests the AI connection with the provided credentials.
func TestAIConnection(provider, model, apiKey string) error {
	if provider != "openai" {
		return fmt.Errorf("unsupported AI provider: %s", provider)
	}

	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}

	// Make a simple API call to test the connection
	// Using a minimal request to the models endpoint
	httpReq, err := http.NewRequest("GET", "https://api.openai.com/v1/models/"+model, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to connect to OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("invalid API key")
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("model '%s' not found or not accessible with this API key", model)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// buildSystemPrompt creates a system prompt with context about PocketBase field types.
func buildSystemPrompt(collectionType string) string {
	basePrompt := `You are a PocketBase schema designer. Generate a valid PocketBase collection schema in JSON format.

Available field types with their required properties:

1. text - Short or long text
   {"type": "text", "name": "title", "required": true, "min": 0, "max": 255}

2. number - Numeric values
   {"type": "number", "name": "price", "required": false, "min": 0, "max": 999999}

3. bool - Boolean values
   {"type": "bool", "name": "is_active", "required": false}

4. email - Email addresses
   {"type": "email", "name": "contact_email", "required": false}

5. url - URLs/links
   {"type": "url", "name": "website", "required": false}

6. editor - Rich text content (HTML)
   {"type": "editor", "name": "content", "required": false}

7. date - Date and datetime values
   {"type": "date", "name": "published_at", "required": false}

8. select - Single or multiple choice (MUST include "values" array and "maxSelect")
   {"type": "select", "name": "status", "required": false, "values": ["draft", "published", "archived"], "maxSelect": 1}
   {"type": "select", "name": "tags", "required": false, "values": ["tech", "news", "tutorial"], "maxSelect": 3}
   NOTE: maxSelect must be <= number of values. Use maxSelect=1 for single choice, maxSelect=number of values for multiple choice.

9. json - Structured JSON data
   {"type": "json", "name": "metadata", "required": false, "maxSize": 0}

10. file - File attachments (MUST include "mimeTypes" array, "maxSelect", "maxSize")
    {"type": "file", "name": "avatar", "required": false, "mimeTypes": ["image/jpeg", "image/png"], "maxSelect": 1, "maxSize": 5242880}

11. relation - Reference to another collection
    {"type": "relation", "name": "author", "required": false, "collectionId": "", "maxSelect": 1, "cascadeDelete": false}

12. autodate - Auto-set timestamps (onCreate, onUpdate, or both)
    {"type": "autodate", "name": "published_at", "onCreate": true, "onUpdate": false}

Output format - Return ONLY valid JSON:
{
  "name": "collection_name",
  "type": "base",
  "fields": [...]
}

CRITICAL RULES:
- Field names: lowercase, alphanumeric, underscores only
- ONLY use the field types listed above: text, number, bool, email, url, editor, date, select, json, file, relation, autodate
- There is NO "tags" type - for tags/categories use "select" type with maxSelect > 1
- For SELECT fields: ALWAYS include "values" as an array of strings and "maxSelect" as a number
- For FILE fields: ALWAYS include "mimeTypes" as array, "maxSelect", and "maxSize"
- Do NOT include system fields (id, created, updated) - added automatically
- For auth collections, email/password fields are added automatically`

	if collectionType == CollectionTypeAuth {
		return basePrompt + "\n\nNote: This is an auth collection. Email and password fields will be added automatically."
	}

	return basePrompt
}

// GenerateSeedDataRequest represents a request to generate seed data for a collection.
type GenerateSeedDataRequest struct {
	CollectionId string `json:"collectionId"`
	Count        int    `json:"count"`
	Description  string `json:"description,omitempty"` // Optional context for data generation
}

// GenerateSeedDataResponse represents the response from seed data generation.
type GenerateSeedDataResponse struct {
	Records []map[string]any `json:"records"`
	Created int              `json:"created"`
	Skipped int              `json:"skipped"`
	Error   string           `json:"error,omitempty"`
}

// SeedFieldInfo represents simplified field info for the AI prompt.
type SeedFieldInfo struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Min       float64  `json:"min,omitempty"`
	Max       float64  `json:"max,omitempty"`
	Values    []string `json:"values,omitempty"` // For select fields
	MaxSelect int      `json:"maxSelect,omitempty"`
}

// GenerateSeedDataFromSchema uses OpenAI to generate realistic sample records for a collection.
func GenerateSeedDataFromSchema(app App, collection *Collection, count int, description string) ([]map[string]any, error) {
	settings := app.Settings()

	if !settings.AI.Enabled {
		return nil, fmt.Errorf("AI features are not enabled")
	}

	if settings.AI.APIKey == "" {
		return nil, fmt.Errorf("AI API key is not configured")
	}

	if settings.AI.Provider != "openai" {
		return nil, fmt.Errorf("unsupported AI provider: %s", settings.AI.Provider)
	}

	if count <= 0 {
		return nil, fmt.Errorf("count must be greater than 0")
	}

	// Cap count to prevent excessive API usage
	if count > 50 {
		count = 50
	}

	// Extract field information for the prompt
	fields := extractSeedFieldsInfo(collection)
	if len(fields) == 0 {
		return nil, fmt.Errorf("collection has no fields suitable for seed data generation")
	}

	// Build the system prompt
	systemPrompt := buildSeedDataSystemPrompt()

	// Build the user prompt
	userPrompt := buildSeedDataUserPrompt(collection.Name, fields, count, description)

	// Prepare OpenAI API request
	openAIReq := map[string]interface{}{
		"model": settings.AI.Model,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": systemPrompt,
			},
			{
				"role":    "user",
				"content": userPrompt,
			},
		},
		"temperature": 0.7, // Slightly higher for more varied data
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", openAIAPIURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", settings.AI.APIKey))

	// Make the request with longer timeout for larger data generation
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
		return nil, fmt.Errorf("failed to read OpenAI response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse OpenAI response
	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	content := openAIResp.Choices[0].Message.Content

	// Parse the records JSON from the response
	var result struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse records JSON: %w", err)
	}

	return result.Records, nil
}

// extractSeedFieldsInfo extracts field information suitable for seed data generation.
// It skips fields that cannot be auto-generated (relations, files, autodate, password).
func extractSeedFieldsInfo(collection *Collection) []SeedFieldInfo {
	var fields []SeedFieldInfo

	// Field types to skip
	skipTypes := map[string]bool{
		FieldTypeRelation: true,
		FieldTypeFile:     true,
		FieldTypeAutodate: true,
	}

	for _, field := range collection.Fields {
		fieldType := field.Type()
		fieldName := field.GetName()

		// Skip system fields
		if fieldName == "id" || fieldName == "created" || fieldName == "updated" {
			continue
		}

		// Skip password field for auth collections
		if fieldName == FieldNamePassword {
			continue
		}

		// Skip relation, file, autodate fields
		if skipTypes[fieldType] {
			continue
		}

		info := SeedFieldInfo{
			Name: fieldName,
			Type: fieldType,
		}

		// Extract type-specific options
		switch f := field.(type) {
		case *NumberField:
			if f.Min != nil {
				info.Min = *f.Min
			}
			if f.Max != nil {
				info.Max = *f.Max
			}
		case *TextField:
			info.Min = float64(f.Min)
			info.Max = float64(f.Max)
		case *SelectField:
			info.Values = f.Values
			info.MaxSelect = f.MaxSelect
		}

		fields = append(fields, info)
	}

	return fields
}

// buildSeedDataSystemPrompt creates the system prompt for seed data generation.
func buildSeedDataSystemPrompt() string {
	return `You are a data generator for PocketBase. Generate realistic, varied sample data based on field schemas.

RULES:
1. Return a JSON object with a "records" array containing the requested number of records
2. Each record should have realistic, varied data appropriate for the field names and types
3. DO NOT include "id", "created", or "updated" fields - they are auto-generated
4. Match data types exactly:
   - text: strings appropriate to the field name (e.g., "title" → article titles, "description" → paragraphs)
   - number: numbers within min/max constraints if provided
   - bool: true or false
   - email: valid email addresses (use example.com domain)
   - url: valid URLs (use example.com domain)
   - editor: HTML content with basic formatting
   - date: ISO 8601 datetime strings (e.g., "2024-01-15 10:30:00.000Z")
   - select: values ONLY from the provided "values" array. If maxSelect=1, use a single string. If maxSelect>1, use an array of strings (up to maxSelect items).
   - json: appropriate JSON objects/arrays based on field name
5. Make data realistic and contextually appropriate:
   - If field is "name", generate realistic names
   - If field is "price", generate realistic prices
   - If field is "status", use typical status values
6. Ensure variety - don't repeat the same values across records
7. Always provide values for all fields to ensure valid records

OUTPUT FORMAT:
{
  "records": [
    { "field1": "value1", "field2": 123, ... },
    { "field1": "value2", "field2": 456, ... }
  ]
}`
}

// buildSeedDataUserPrompt creates the user prompt for seed data generation.
func buildSeedDataUserPrompt(collectionName string, fields []SeedFieldInfo, count int, description string) string {
	fieldsJSON, _ := json.MarshalIndent(fields, "", "  ")

	prompt := fmt.Sprintf(`Generate EXACTLY %d sample records for a "%s" collection.

IMPORTANT: You MUST generate exactly %d records - no more, no less.

Field Schema:
%s`, count, collectionName, count, string(fieldsJSON))

	if description != "" {
		prompt += fmt.Sprintf(`

Context/Description: %s

Use this context to make the generated data more relevant and realistic.`, description)
	}

	prompt += fmt.Sprintf(`

Return a JSON object with a "records" array containing EXACTLY %d generated records.`, count)

	return prompt
}

// =====================================================
// HYBRID SEED DATA GENERATION
// =====================================================

// GenerateSeedDataHybrid generates seed data using the optimal strategy based on count.
// For count <= HybridThreshold (20): Uses pure AI generation
// For count > HybridThreshold: Uses AI archetypes + gofakeit multiplexing
func GenerateSeedDataHybrid(app App, collection *Collection, count int, description string) ([]map[string]any, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be greater than 0")
	}

	// For small counts, use pure AI (existing behavior)
	if count <= HybridThreshold {
		return GenerateSeedDataFromSchema(app, collection, count, description)
	}

	// For larger counts, use hybrid approach
	return generateSeedDataHybridInternal(app, collection, count, description)
}

// generateSeedDataHybridInternal implements the hybrid AI + gofakeit approach
func generateSeedDataHybridInternal(app App, collection *Collection, count int, description string) ([]map[string]any, error) {
	// Extract field information
	fields := extractSeedFieldsInfo(collection)
	if len(fields) == 0 {
		return nil, fmt.Errorf("collection has no fields suitable for seed data generation")
	}

	// Compute schema hash for cache validation
	schemaHash := computeSchemaHash(fields)

	// Try to get cached archetypes
	var archetypes []map[string]any
	cached, found := globalArchetypeCache.Get(collection.Id, schemaHash)

	if found {
		archetypes = cached.Archetypes
	} else {
		// Generate new archetypes using AI
		var err error
		archetypes, err = generateArchetypes(app, collection, fields, description)
		if err != nil {
			return nil, fmt.Errorf("failed to generate archetypes: %w", err)
		}

		// Cache the archetypes
		globalArchetypeCache.Set(collection.Id, &CachedArchetypes{
			SchemaHash: schemaHash,
			Archetypes: archetypes,
			Fields:     fields,
			CreatedAt:  time.Now(),
		})
	}

	// Multiply archetypes using gofakeit
	records := multiplyArchetypes(archetypes, fields, count)

	return records, nil
}

// generateArchetypes uses AI to generate diverse archetype records
func generateArchetypes(app App, collection *Collection, fields []SeedFieldInfo, description string) ([]map[string]any, error) {
	settings := app.Settings()

	if !settings.AI.Enabled {
		return nil, fmt.Errorf("AI features are not enabled")
	}

	if settings.AI.APIKey == "" {
		return nil, fmt.Errorf("AI API key is not configured")
	}

	// Build specialized prompt for archetypes
	systemPrompt := buildArchetypeSystemPrompt()
	userPrompt := buildArchetypeUserPrompt(collection.Name, fields, description)

	// Prepare OpenAI API request
	openAIReq := map[string]interface{}{
		"model": settings.AI.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.8, // Higher temperature for more diverse archetypes
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	reqBody, err := json.Marshal(openAIReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", openAIAPIURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", settings.AI.APIKey))

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to call OpenAI API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read OpenAI response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var openAIResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(respBody, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	if len(openAIResp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	var result struct {
		Archetypes []map[string]any `json:"archetypes"`
	}
	if err := json.Unmarshal([]byte(openAIResp.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse archetypes JSON: %w", err)
	}

	if len(result.Archetypes) == 0 {
		return nil, fmt.Errorf("AI returned no archetypes")
	}

	return result.Archetypes, nil
}

// buildArchetypeSystemPrompt creates the system prompt for archetype generation
func buildArchetypeSystemPrompt() string {
	return `You are a data archetype generator for PocketBase. Generate DIVERSE archetype records that represent different "personas" or "categories" of data.

RULES:
1. Return a JSON object with an "archetypes" array containing exactly 12 DIVERSE records
2. Each archetype should represent a DISTINCT category, persona, or style
3. For text fields that appear to be creative content (bio, description, content, summary, etc.):
   - Write full, realistic, varied content in different styles and tones
   - Some formal, some casual, some brief, some detailed
4. For name-like fields (name, title, username, etc.):
   - Provide placeholder patterns like "{{NAME}}", "{{TITLE}}", "{{USERNAME}}"
   - These will be replaced with generated values
5. For email fields: use "{{EMAIL}}" as placeholder
6. For URL fields: use "{{URL}}" as placeholder
7. DO NOT include "id", "created", or "updated" fields
8. Match data types exactly:
   - text: strings (use {{PLACEHOLDER}} for unique fields, real content for creative fields)
   - number: numbers within constraints
   - bool: true or false
   - email: "{{EMAIL}}"
   - url: "{{URL}}"
   - editor: HTML content with basic formatting
   - date: ISO 8601 datetime strings
   - select: values ONLY from the provided "values" array
   - json: appropriate JSON objects

DIVERSITY IS KEY:
- Vary the tone: professional, casual, enthusiastic, minimalist
- Vary the length: some brief, some detailed
- Vary the content: different topics, perspectives, styles
- Each archetype should feel like a different "person" wrote it

OUTPUT FORMAT:
{
  "archetypes": [
    { "field1": "value or {{PLACEHOLDER}}", ... },
    ...12 total archetypes...
  ]
}`
}

// buildArchetypeUserPrompt creates the user prompt for archetype generation
func buildArchetypeUserPrompt(collectionName string, fields []SeedFieldInfo, description string) string {
	fieldsJSON, _ := json.MarshalIndent(fields, "", "  ")

	prompt := fmt.Sprintf(`Generate 12 DIVERSE archetype records for a "%s" collection.

These archetypes will be used as templates to generate thousands of records, so make them:
- DIVERSE in style, tone, and content
- REPRESENTATIVE of different user personas or data categories
- HIGH QUALITY with realistic, contextual content

Field Schema:
%s`, collectionName, string(fieldsJSON))

	if description != "" {
		prompt += fmt.Sprintf(`

Context/Description: %s

Use this context to make archetypes more relevant and realistic.`, description)
	}

	prompt += `

Remember:
- Use {{NAME}}, {{EMAIL}}, {{URL}}, {{USERNAME}}, {{TITLE}} placeholders for fields that should be unique per record
- Write actual content for creative fields (bio, description, content, etc.)
- Return exactly 12 diverse archetypes in the "archetypes" array`

	return prompt
}

// multiplyArchetypes generates records by mutating archetypes with gofakeit
// Uses parallel workers for large counts to maximize throughput
func multiplyArchetypes(archetypes []map[string]any, fields []SeedFieldInfo, count int) []map[string]any {
	// Build a field type map for quick lookup
	fieldTypes := make(map[string]SeedFieldInfo)
	for _, f := range fields {
		fieldTypes[f.Name] = f
	}

	// For small counts, use simple sequential generation
	if count <= 1000 {
		records := make([]map[string]any, 0, count)
		for i := 0; i < count; i++ {
			archetype := archetypes[rand.Intn(len(archetypes))]
			record := mutateArchetype(archetype, fieldTypes)
			records = append(records, record)
		}
		return records
	}

	// For large counts, use parallel generation with worker pool
	return multiplyArchetypesParallel(archetypes, fieldTypes, count)
}

// multiplyArchetypesParallel generates records using multiple goroutines
func multiplyArchetypesParallel(archetypes []map[string]any, fieldTypes map[string]SeedFieldInfo, count int) []map[string]any {
	// Determine number of workers (use available CPUs, cap at 8)
	numWorkers := 8
	
	// Pre-allocate result slice
	records := make([]map[string]any, count)
	
	// Calculate records per worker
	chunkSize := count / numWorkers
	remainder := count % numWorkers

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	startIdx := 0
	for w := 0; w < numWorkers; w++ {
		// Distribute remainder among first workers
		workerCount := chunkSize
		if w < remainder {
			workerCount++
		}
		
		endIdx := startIdx + workerCount
		
		// Launch worker goroutine
		go func(start, end int) {
			defer wg.Done()
			
			// Each worker has its own random source for thread safety
			localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(start)))
			
			for i := start; i < end; i++ {
				// Pick a random archetype
				archetype := archetypes[localRand.Intn(len(archetypes))]
				// Generate record (mutateArchetype is thread-safe with local rand)
				records[i] = mutateArchetypeWithRand(archetype, fieldTypes, localRand)
			}
		}(startIdx, endIdx)
		
		startIdx = endIdx
	}

	wg.Wait()
	return records
}

// mutateArchetypeWithRand is a thread-safe version using a local random source
func mutateArchetypeWithRand(archetype map[string]any, fieldTypes map[string]SeedFieldInfo, localRand *rand.Rand) map[string]any {
	record := make(map[string]any)

	for fieldName, value := range archetype {
		fieldInfo, hasInfo := fieldTypes[fieldName]

		switch v := value.(type) {
		case string:
			record[fieldName] = mutateStringFieldWithRand(v, fieldName, fieldInfo, hasInfo, localRand)
		case float64:
			if hasInfo && fieldInfo.Type == FieldTypeNumber {
				record[fieldName] = mutateNumberFieldWithRand(fieldInfo, localRand)
			} else {
				record[fieldName] = v
			}
		case bool:
			if localRand.Float32() < 0.3 {
				record[fieldName] = !v
			} else {
				record[fieldName] = v
			}
		case []interface{}:
			if hasInfo && fieldInfo.Type == FieldTypeSelect && len(fieldInfo.Values) > 0 {
				record[fieldName] = mutateSelectFieldWithRand(fieldInfo, localRand)
			} else {
				record[fieldName] = v
			}
		default:
			record[fieldName] = value
		}
	}

	return record
}

// mutateStringFieldWithRand is thread-safe string mutation
func mutateStringFieldWithRand(value, fieldName string, fieldInfo SeedFieldInfo, hasInfo bool, localRand *rand.Rand) string {
	result := value

	// Replace placeholders using gofakeit (which is thread-safe)
	if strings.Contains(result, "{{NAME}}") {
		result = strings.ReplaceAll(result, "{{NAME}}", gofakeit.Name())
	}
	if strings.Contains(result, "{{FIRSTNAME}}") {
		result = strings.ReplaceAll(result, "{{FIRSTNAME}}", gofakeit.FirstName())
	}
	if strings.Contains(result, "{{LASTNAME}}") {
		result = strings.ReplaceAll(result, "{{LASTNAME}}", gofakeit.LastName())
	}
	if strings.Contains(result, "{{EMAIL}}") {
		result = strings.ReplaceAll(result, "{{EMAIL}}", gofakeit.Email())
	}
	if strings.Contains(result, "{{URL}}") {
		result = strings.ReplaceAll(result, "{{URL}}", gofakeit.URL())
	}
	if strings.Contains(result, "{{USERNAME}}") {
		result = strings.ReplaceAll(result, "{{USERNAME}}", gofakeit.Username())
	}
	if strings.Contains(result, "{{TITLE}}") {
		result = strings.ReplaceAll(result, "{{TITLE}}", gofakeit.Sentence(localRand.Intn(5)+3))
	}
	if strings.Contains(result, "{{COMPANY}}") {
		result = strings.ReplaceAll(result, "{{COMPANY}}", gofakeit.Company())
	}
	if strings.Contains(result, "{{CITY}}") {
		result = strings.ReplaceAll(result, "{{CITY}}", gofakeit.City())
	}
	if strings.Contains(result, "{{COUNTRY}}") {
		result = strings.ReplaceAll(result, "{{COUNTRY}}", gofakeit.Country())
	}
	if strings.Contains(result, "{{JOBTITLE}}") {
		result = strings.ReplaceAll(result, "{{JOBTITLE}}", gofakeit.JobTitle())
	}
	if strings.Contains(result, "{{PHONE}}") {
		result = strings.ReplaceAll(result, "{{PHONE}}", gofakeit.Phone())
	}

	if hasInfo {
		switch fieldInfo.Type {
		case FieldTypeEmail:
			if result == "{{EMAIL}}" || result == "" {
				return gofakeit.Email()
			}
		case FieldTypeURL:
			if result == "{{URL}}" || result == "" {
				return gofakeit.URL()
			}
		case FieldTypeSelect:
			if len(fieldInfo.Values) > 0 && fieldInfo.MaxSelect <= 1 {
				return fieldInfo.Values[localRand.Intn(len(fieldInfo.Values))]
			}
		}
	}

	lowerName := strings.ToLower(fieldName)
	if result == value {
		if strings.Contains(lowerName, "email") {
			return gofakeit.Email()
		}
		if strings.Contains(lowerName, "phone") {
			return gofakeit.Phone()
		}
		if strings.Contains(lowerName, "username") || lowerName == "user" {
			return gofakeit.Username()
		}
	}

	return result
}

// mutateNumberFieldWithRand generates a random number with local rand
func mutateNumberFieldWithRand(fieldInfo SeedFieldInfo, localRand *rand.Rand) float64 {
	min := fieldInfo.Min
	max := fieldInfo.Max
	if min == 0 && max == 0 {
		min = 0
		max = 1000
	} else if max == 0 {
		max = min + 1000
	}
	return min + localRand.Float64()*(max-min)
}

// mutateSelectFieldWithRand picks random values with local rand
func mutateSelectFieldWithRand(fieldInfo SeedFieldInfo, localRand *rand.Rand) interface{} {
	if len(fieldInfo.Values) == 0 {
		return ""
	}

	if fieldInfo.MaxSelect <= 1 {
		return fieldInfo.Values[localRand.Intn(len(fieldInfo.Values))]
	}

	numSelections := localRand.Intn(fieldInfo.MaxSelect) + 1
	if numSelections > len(fieldInfo.Values) {
		numSelections = len(fieldInfo.Values)
	}

	shuffled := make([]string, len(fieldInfo.Values))
	copy(shuffled, fieldInfo.Values)
	localRand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	return shuffled[:numSelections]
}

// mutateArchetype creates a new record by replacing placeholders and randomizing fields
func mutateArchetype(archetype map[string]any, fieldTypes map[string]SeedFieldInfo) map[string]any {
	record := make(map[string]any)

	for fieldName, value := range archetype {
		fieldInfo, hasInfo := fieldTypes[fieldName]

		// Process based on field type and value
		switch v := value.(type) {
		case string:
			record[fieldName] = mutateStringField(v, fieldName, fieldInfo, hasInfo)
		case float64:
			if hasInfo && fieldInfo.Type == FieldTypeNumber {
				record[fieldName] = mutateNumberField(fieldInfo)
			} else {
				record[fieldName] = v
			}
		case bool:
			// Randomly flip booleans for variety
			if rand.Float32() < 0.3 {
				record[fieldName] = !v
			} else {
				record[fieldName] = v
			}
		case []interface{}:
			// Handle select fields with multiple values
			if hasInfo && fieldInfo.Type == FieldTypeSelect && len(fieldInfo.Values) > 0 {
				record[fieldName] = mutateSelectField(fieldInfo)
			} else {
				record[fieldName] = v
			}
		default:
			// Keep JSON and other complex types as-is
			record[fieldName] = value
		}
	}

	return record
}

// mutateStringField handles string field mutation with placeholder replacement
func mutateStringField(value, fieldName string, fieldInfo SeedFieldInfo, hasInfo bool) string {
	// Replace placeholders
	result := value

	// Common placeholder replacements
	if strings.Contains(result, "{{NAME}}") {
		result = strings.ReplaceAll(result, "{{NAME}}", gofakeit.Name())
	}
	if strings.Contains(result, "{{FIRSTNAME}}") {
		result = strings.ReplaceAll(result, "{{FIRSTNAME}}", gofakeit.FirstName())
	}
	if strings.Contains(result, "{{LASTNAME}}") {
		result = strings.ReplaceAll(result, "{{LASTNAME}}", gofakeit.LastName())
	}
	if strings.Contains(result, "{{EMAIL}}") {
		result = strings.ReplaceAll(result, "{{EMAIL}}", gofakeit.Email())
	}
	if strings.Contains(result, "{{URL}}") {
		result = strings.ReplaceAll(result, "{{URL}}", gofakeit.URL())
	}
	if strings.Contains(result, "{{USERNAME}}") {
		result = strings.ReplaceAll(result, "{{USERNAME}}", gofakeit.Username())
	}
	if strings.Contains(result, "{{TITLE}}") {
		result = strings.ReplaceAll(result, "{{TITLE}}", gofakeit.Sentence(rand.Intn(5)+3))
	}
	if strings.Contains(result, "{{COMPANY}}") {
		result = strings.ReplaceAll(result, "{{COMPANY}}", gofakeit.Company())
	}
	if strings.Contains(result, "{{CITY}}") {
		result = strings.ReplaceAll(result, "{{CITY}}", gofakeit.City())
	}
	if strings.Contains(result, "{{COUNTRY}}") {
		result = strings.ReplaceAll(result, "{{COUNTRY}}", gofakeit.Country())
	}
	if strings.Contains(result, "{{JOBTITLE}}") {
		result = strings.ReplaceAll(result, "{{JOBTITLE}}", gofakeit.JobTitle())
	}
	if strings.Contains(result, "{{PHONE}}") {
		result = strings.ReplaceAll(result, "{{PHONE}}", gofakeit.Phone())
	}

	// If still has placeholders or is a known unique field type, generate fresh
	if hasInfo {
		switch fieldInfo.Type {
		case FieldTypeEmail:
			if result == "{{EMAIL}}" || result == "" {
				return gofakeit.Email()
			}
		case FieldTypeURL:
			if result == "{{URL}}" || result == "" {
				return gofakeit.URL()
			}
		case FieldTypeSelect:
			if len(fieldInfo.Values) > 0 {
				if fieldInfo.MaxSelect <= 1 {
					return fieldInfo.Values[rand.Intn(len(fieldInfo.Values))]
				}
			}
		}
	}

	// Check field name patterns for additional mutation
	lowerName := strings.ToLower(fieldName)
	if result == value { // Not mutated yet
		if strings.Contains(lowerName, "email") {
			return gofakeit.Email()
		}
		if strings.Contains(lowerName, "phone") {
			return gofakeit.Phone()
		}
		if strings.Contains(lowerName, "username") || lowerName == "user" {
			return gofakeit.Username()
		}
	}

	return result
}

// mutateNumberField generates a random number within field constraints
func mutateNumberField(fieldInfo SeedFieldInfo) float64 {
	min := fieldInfo.Min
	max := fieldInfo.Max

	// Set reasonable defaults if not specified
	if min == 0 && max == 0 {
		min = 0
		max = 1000
	} else if max == 0 {
		max = min + 1000
	}

	return min + rand.Float64()*(max-min)
}

// mutateSelectField picks random values from a select field
func mutateSelectField(fieldInfo SeedFieldInfo) interface{} {
	if len(fieldInfo.Values) == 0 {
		return ""
	}

	if fieldInfo.MaxSelect <= 1 {
		// Single select - return a string
		return fieldInfo.Values[rand.Intn(len(fieldInfo.Values))]
	}

	// Multi-select - return an array
	numSelections := rand.Intn(fieldInfo.MaxSelect) + 1
	if numSelections > len(fieldInfo.Values) {
		numSelections = len(fieldInfo.Values)
	}

	// Shuffle and pick
	shuffled := make([]string, len(fieldInfo.Values))
	copy(shuffled, fieldInfo.Values)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	return shuffled[:numSelections]
}

// mutateDateField generates a random date within a reasonable range
func mutateDateField() string {
	// Generate a date within the last 2 years
	minDate := time.Now().AddDate(-2, 0, 0)
	maxDate := time.Now()

	delta := maxDate.Sub(minDate)
	randomDelta := time.Duration(rand.Int63n(int64(delta)))

	randomDate := minDate.Add(randomDelta)
	return randomDate.Format("2006-01-02 15:04:05.000Z")
}

