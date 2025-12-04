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
func aiGenerateSeedData(e *core.RequestEvent) error {
	var req struct {
		CollectionId string `json:"collectionId"`
		Count        int    `json:"count"`
		Description  string `json:"description"`
	}

	if err := e.BindBody(&req); err != nil {
		return e.BadRequestError("Failed to load the submitted data due to invalid formatting.", err)
	}

	// Validate request
	if err := validation.ValidateStruct(&req,
		validation.Field(&req.CollectionId, validation.Required),
		validation.Field(&req.Count, validation.Required, validation.Min(1), validation.Max(50)),
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

	// Generate seed data using AI service
	records, err := core.GenerateSeedDataFromSchema(e.App, collection, req.Count, req.Description)
	if err != nil {
		return e.BadRequestError("Failed to generate seed data: "+err.Error(), nil)
	}

	// Create the records in the database
	created := 0
	skipped := 0
	var creationErrors []string

	for i, recordData := range records {
		record := core.NewRecord(collection)
		form := forms.NewRecordUpsert(e.App, record)
		form.GrantSuperuserAccess()
		form.Load(recordData)

		if err := form.Submit(); err != nil {
			skipped++
			creationErrors = append(creationErrors,
				fmt.Sprintf("Record %d: %s", i+1, err.Error()))
			continue
		}
		created++
	}

	response := map[string]interface{}{
		"created": created,
		"skipped": skipped,
		"total":   len(records),
	}

	if len(creationErrors) > 0 && len(creationErrors) <= 5 {
		response["errors"] = creationErrors
	} else if len(creationErrors) > 5 {
		response["errors"] = append(creationErrors[:5], "... and more")
	}

	return e.JSON(http.StatusOK, response)
}

