package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	openAIAPIURL = "https://api.openai.com/v1/chat/completions"
)

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
- For SELECT fields: ALWAYS include "values" as an array of strings and "maxSelect" as a number
- For FILE fields: ALWAYS include "mimeTypes" as array, "maxSelect", and "maxSize"
- Do NOT include system fields (id, created, updated) - added automatically
- For auth collections, email/password fields are added automatically
- If user mentions "tags" or multiple choices, use select with maxSelect > 1`

	if collectionType == CollectionTypeAuth {
		return basePrompt + "\n\nNote: This is an auth collection. Email and password fields will be added automatically."
	}

	return basePrompt
}

