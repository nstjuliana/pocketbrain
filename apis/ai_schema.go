package apis

import (
	"errors"
	"fmt"
	"net/http"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/forms"
	"github.com/pocketbase/pocketbase/tools/router"
)

// bindAIApi registers the AI API endpoints.
func bindAIApi(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	subGroup := rg.Group("/ai").Bind(RequireSuperuserAuth())
	subGroup.POST("/generate-schema", aiGenerateSchema)
	subGroup.POST("/test-connection", aiTestConnection)
	subGroup.POST("/generate-seed-data", aiGenerateSeedData)
	subGroup.POST("/generate-embeddings", aiGenerateEmbeddings)
	subGroup.POST("/find-similar", aiFindSimilar)
	subGroup.GET("/embedding-stats", aiGetEmbeddingStats)
	subGroup.GET("/embedding-cache-stats", aiGetEmbeddingCacheStats)
	subGroup.POST("/clear-embedding-cache", aiClearEmbeddingCache)
}

func aiGenerateSchema(e *core.RequestEvent) error {
	var req core.GenerateSchemaRequest

	if err := e.BindBody(&req); err != nil {
		return e.BadRequestError("Failed to load the submitted data due to invalid formatting.", err)
	}

	// Validate request
	if err := validation.ValidateStruct(&req,
		validation.Field(&req.Prompt, validation.Required, validation.Length(1, 2000)),
		validation.Field(&req.CollectionType, validation.In("base", "auth", "view")),
	); err != nil {
		return e.BadRequestError("Invalid request data.", err)
	}

	// Set default collection type if not provided
	if req.CollectionType == "" {
		req.CollectionType = core.CollectionTypeBase
	}

	// Generate schema using AI service
	collection, err := core.GenerateSchemaFromPrompt(e.App, req)
	if err != nil {
		// Check if it's a validation error
		var validationErrors validation.Errors
		if errors.As(err, &validationErrors) {
			return e.BadRequestError("Failed to generate schema.", validationErrors)
		}

		// Other errors
		return e.BadRequestError("Failed to generate schema. "+err.Error(), nil)
	}

	return execAfterSuccessTx(true, e.App, func() error {
		return e.JSON(http.StatusOK, collection)
	})
}

// aiTestConnection tests the AI connection using provided credentials.
func aiTestConnection(e *core.RequestEvent) error {
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		APIKey   string `json:"apiKey"`
	}

	if err := e.BindBody(&req); err != nil {
		return e.BadRequestError("Failed to load the submitted data due to invalid formatting.", err)
	}

	// Validate request
	if err := validation.ValidateStruct(&req,
		validation.Field(&req.Provider, validation.Required, validation.In("openai")),
		validation.Field(&req.Model, validation.Required),
		validation.Field(&req.APIKey, validation.Required),
	); err != nil {
		return e.BadRequestError("Invalid request data.", err)
	}

	// Test the connection using the provided credentials
	err := core.TestAIConnection(req.Provider, req.Model, req.APIKey)
	if err != nil {
		return e.BadRequestError("Connection test failed: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Connection successful",
	})
}

// aiGenerateSeedData generates sample records for a collection using AI.
// For counts <= 20: Uses pure AI generation
// For counts > 20: Uses hybrid AI archetypes + gofakeit multiplexing for speed
func aiGenerateSeedData(e *core.RequestEvent) error {
	var req struct {
		CollectionId string `json:"collectionId"`
		Count        int    `json:"count"`
		Description  string `json:"description"`
	}

	if err := e.BindBody(&req); err != nil {
		return e.BadRequestError("Failed to load the submitted data due to invalid formatting.", err)
	}

	// Validate request - now supports up to 1,000,000 records
	if err := validation.ValidateStruct(&req,
		validation.Field(&req.CollectionId, validation.Required),
		validation.Field(&req.Count, validation.Required, validation.Min(1), validation.Max(1000000)),
	); err != nil {
		return e.BadRequestError("Invalid request data.", err)
	}

	// Find the collection
	collection, err := e.App.FindCollectionByNameOrId(req.CollectionId)
	if err != nil {
		return e.NotFoundError("Collection not found.", err)
	}

	// Don't allow seed data for view collections
	if collection.IsView() {
		return e.BadRequestError("Cannot generate seed data for view collections.", nil)
	}

	// Generate seed data using hybrid AI service (auto-switches based on count)
	records, err := core.GenerateSeedDataHybrid(e.App, collection, req.Count, req.Description)
	if err != nil {
		return e.BadRequestError("Failed to generate seed data: "+err.Error(), nil)
	}

	// Determine which mode was used
	mode := "pure_ai"
	if req.Count > core.HybridThreshold {
		mode = "hybrid"
	}

	// Create the records in the database using transaction for better performance
	created := 0
	skipped := 0
	var creationErrors []string

	// Use batched transaction for large counts
	batchSize := 100
	if req.Count > 1000 {
		batchSize = 500
	}

	for i := 0; i < len(records); i += batchSize {
		end := i + batchSize
		if end > len(records) {
			end = len(records)
		}
		batch := records[i:end]

		err := e.App.RunInTransaction(func(txApp core.App) error {
			for j, recordData := range batch {
				record := core.NewRecord(collection)
				form := forms.NewRecordUpsert(txApp, record)
				form.GrantSuperuserAccess()
				form.Load(recordData)

				if err := form.Submit(); err != nil {
					skipped++
					if len(creationErrors) < 10 {
						creationErrors = append(creationErrors,
							fmt.Sprintf("Record %d: %s", i+j+1, err.Error()))
					}
					continue
				}
				created++
			}
			return nil
		})

		if err != nil {
			// Log transaction error but continue with other batches
			creationErrors = append(creationErrors,
				fmt.Sprintf("Batch %d-%d transaction error: %s", i+1, end, err.Error()))
		}
	}

	response := map[string]interface{}{
		"created": created,
		"skipped": skipped,
		"total":   len(records),
		"mode":    mode,
	}

	if len(creationErrors) > 0 && len(creationErrors) <= 5 {
		response["errors"] = creationErrors
	} else if len(creationErrors) > 5 {
		response["errors"] = append(creationErrors[:5], fmt.Sprintf("... and %d more", len(creationErrors)-5))
	}

	return e.JSON(http.StatusOK, response)
}

// aiGenerateEmbeddings generates vector embeddings for records in a collection.
func aiGenerateEmbeddings(e *core.RequestEvent) error {
	var req core.EmbeddingRequest

	if err := e.BindBody(&req); err != nil {
		return e.BadRequestError("Failed to load the submitted data due to invalid formatting.", err)
	}

	// Validate request - CollectionId is always required
	// FieldName is only required for field-level mode (not record mode)
	if req.CollectionId == "" {
		return e.BadRequestError("collectionId is required.", nil)
	}

	// For field mode (default), fieldName is required
	if req.Mode != core.EmbeddingModeRecord && req.FieldName == "" {
		return e.BadRequestError("fieldName is required for field-level embedding mode.", nil)
	}

	// Generate embeddings
	response, err := core.GenerateEmbeddings(e.App, req)
	if err != nil {
		return e.BadRequestError("Failed to generate embeddings: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, response)
}

// aiFindSimilar finds records similar to a given text or record.
func aiFindSimilar(e *core.RequestEvent) error {
	var req core.FindSimilarRequest

	if err := e.BindBody(&req); err != nil {
		return e.BadRequestError("Failed to load the submitted data due to invalid formatting.", err)
	}

	// Validate request - CollectionId is always required
	if req.CollectionId == "" {
		return e.BadRequestError("collectionId is required.", nil)
	}

	// For field mode (default), fieldName is required
	if req.Mode != core.EmbeddingModeRecord && req.FieldName == "" {
		return e.BadRequestError("fieldName is required for field-level search mode.", nil)
	}

	// Validate limit
	if req.Limit < 0 || req.Limit > 100 {
		return e.BadRequestError("limit must be between 1 and 100.", nil)
	}

	// Require either text or recordId
	if req.Text == "" && req.RecordId == "" {
		return e.BadRequestError("Either 'text' or 'recordId' must be provided.", nil)
	}

	// Set default limit
	if req.Limit == 0 {
		req.Limit = 10
	}

	// Find similar records
	response, err := core.FindSimilarRecords(e.App, req)
	if err != nil {
		return e.BadRequestError("Failed to find similar records: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, response)
}

// aiGetEmbeddingStats returns embedding statistics for a collection/field.
func aiGetEmbeddingStats(e *core.RequestEvent) error {
	collectionId := e.Request.URL.Query().Get("collectionId")
	fieldName := e.Request.URL.Query().Get("fieldName")

	if collectionId == "" || fieldName == "" {
		return e.BadRequestError("Both 'collectionId' and 'fieldName' query parameters are required.", nil)
	}

	stats, err := core.GetEmbeddingStatsForField(e.App, collectionId, fieldName)
	if err != nil {
		return e.BadRequestError("Failed to get embedding stats: "+err.Error(), nil)
	}

	return e.JSON(http.StatusOK, stats)
}

// aiGetEmbeddingCacheStats returns statistics about the embedding cache.
func aiGetEmbeddingCacheStats(e *core.RequestEvent) error {
	stats := core.GetEmbeddingCacheStats()
	return e.JSON(http.StatusOK, stats)
}

// aiClearEmbeddingCache clears the embedding cache.
func aiClearEmbeddingCache(e *core.RequestEvent) error {
	core.ClearEmbeddingCache()
	return e.JSON(http.StatusOK, map[string]string{"status": "ok", "message": "Embedding cache cleared"})
}

