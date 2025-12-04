package apis

import (
	"errors"
	"net/http"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

// bindAIApi registers the AI API endpoints.
func bindAIApi(app core.App, rg *router.RouterGroup[*core.RequestEvent]) {
	subGroup := rg.Group("/ai").Bind(RequireSuperuserAuth())
	subGroup.POST("/generate-schema", aiGenerateSchema)
	subGroup.POST("/test-connection", aiTestConnection)
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

