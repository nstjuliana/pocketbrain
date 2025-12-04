package core

import (
	"fmt"
	"strings"
)

const (
	// EmbeddingsCollectionName is the name of the system collection for storing embeddings
	EmbeddingsCollectionName = "_embeddings"
)

// EnsureEmbeddingsCollection creates the _embeddings system collection if it doesn't exist.
// Returns the embeddings collection.
// This function is safe to call concurrently - if multiple goroutines try to create
// the collection simultaneously, all but one will find it already exists.
func EnsureEmbeddingsCollection(app App) (*Collection, error) {
	// Try to find existing collection
	collection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
	if err == nil {
		return collection, nil
	}

	// Create the embeddings collection
	collection = NewCollection(CollectionTypeBase, EmbeddingsCollectionName)
	collection.System = true

	// Add fields for the embeddings collection
	collection.Fields.Add(&TextField{
		Name:     "record_id",
		Required: true,
		System:   true,
	})

	collection.Fields.Add(&TextField{
		Name:     "collection_id",
		Required: true,
		System:   true,
	})

	collection.Fields.Add(&TextField{
		Name:     "field_name",
		Required: true,
		System:   true,
	})

	// Embedding stored as BLOB (bytes) via JSON field for binary data
	collection.Fields.Add(&JSONField{
		Name:     "embedding",
		Required: true,
		System:   true,
	})

	collection.Fields.Add(&TextField{
		Name:     "model",
		Required: true,
		System:   true,
	})

	collection.Fields.Add(&NumberField{
		Name:     "dimensions",
		Required: true,
		System:   true,
	})

	// Add indexes for efficient lookup
	collection.Indexes = []string{
		"CREATE UNIQUE INDEX idx_embeddings_record_field ON _embeddings (record_id, field_name)",
		"CREATE INDEX idx_embeddings_collection_field ON _embeddings (collection_id, field_name)",
	}

	// Save the collection
	if err := app.Save(collection); err != nil {
		// Handle race condition: if another goroutine created the collection
		// at the same time, we'll get a constraint/uniqueness error. In that case,
		// just find and return the existing collection.
		errStr := err.Error()
		if strings.Contains(errStr, "UNIQUE constraint failed") ||
			strings.Contains(errStr, "already exists") ||
			strings.Contains(errStr, "must be unique") {
			collection, findErr := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
			if findErr == nil {
				return collection, nil
			}
		}
		return nil, fmt.Errorf("failed to create embeddings collection: %w", err)
	}

	return collection, nil
}

// DeleteEmbeddingsForRecord deletes all embeddings associated with a record
func DeleteEmbeddingsForRecord(app App, recordId string) error {
	collection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	records, err := app.FindRecordsByFilter(
		collection.Id,
		"record_id = {:recordId}",
		"",
		0, // Get all
		0,
		map[string]any{
			"recordId": recordId,
		},
	)
	if err != nil {
		return nil
	}

	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return fmt.Errorf("failed to delete embedding: %w", err)
		}
	}

	return nil
}

// DeleteEmbeddingsForCollection deletes all embeddings for records in a collection
func DeleteEmbeddingsForCollection(app App, collectionId string) error {
	collection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	records, err := app.FindRecordsByFilter(
		collection.Id,
		"collection_id = {:collectionId}",
		"",
		0, // Get all
		0,
		map[string]any{
			"collectionId": collectionId,
		},
	)
	if err != nil {
		return nil
	}

	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return fmt.Errorf("failed to delete embedding: %w", err)
		}
	}

	// Invalidate cache for this collection
	embeddingCache.InvalidateCollection(collectionId)

	return nil
}

// DeleteEmbeddingsForField deletes all embeddings for a specific field in a collection
func DeleteEmbeddingsForField(app App, collectionId, fieldName string) error {
	collection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	records, err := app.FindRecordsByFilter(
		collection.Id,
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
		return nil
	}

	for _, record := range records {
		if err := app.Delete(record); err != nil {
			return fmt.Errorf("failed to delete embedding: %w", err)
		}
	}

	// Invalidate cache for this collection/field
	embeddingCache.Invalidate(collectionId, fieldName)

	return nil
}

// GetEmbeddingStats returns statistics about embeddings for a collection/field
type EmbeddingStats struct {
	TotalRecords       int `json:"totalRecords"`
	EmbeddedRecords    int `json:"embeddedRecords"`
	NotEmbeddedRecords int `json:"notEmbeddedRecords"`
}

// GetEmbeddingStatsForField returns embedding statistics for a specific field
func GetEmbeddingStatsForField(app App, collectionId, fieldName string) (*EmbeddingStats, error) {
	// Count total records in the source collection
	collection, err := app.FindCollectionByNameOrId(collectionId)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %w", err)
	}

	allRecords, err := app.FindAllRecords(collection.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to count records: %w", err)
	}
	totalRecords := len(allRecords)

	// Count embedded records
	embeddingsCollection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
	if err != nil {
		// No embeddings yet
		return &EmbeddingStats{
			TotalRecords:       totalRecords,
			EmbeddedRecords:    0,
			NotEmbeddedRecords: totalRecords,
		}, nil
	}

	embeddedRecords, err := app.FindRecordsByFilter(
		embeddingsCollection.Id,
		"collection_id = {:collectionId} && field_name = {:fieldName}",
		"",
		0,
		0,
		map[string]any{
			"collectionId": collection.Id, // Use resolved ID, not the input param
			"fieldName":    fieldName,
		},
	)
	if err != nil {
		embeddedRecords = []*Record{}
	}

	return &EmbeddingStats{
		TotalRecords:       totalRecords,
		EmbeddedRecords:    len(embeddedRecords),
		NotEmbeddedRecords: totalRecords - len(embeddedRecords),
	}, nil
}

// GetPendingEmbeddingRecordIds returns IDs of records that don't have embeddings yet
func GetPendingEmbeddingRecordIds(app App, collectionId, fieldName string) ([]string, error) {
	// Get the source collection
	collection, err := app.FindCollectionByNameOrId(collectionId)
	if err != nil {
		return nil, fmt.Errorf("collection not found: %w", err)
	}

	// Get all record IDs from the source collection
	allRecords, err := app.FindAllRecords(collection.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch records: %w", err)
	}

	allIds := make(map[string]bool)
	for _, record := range allRecords {
		allIds[record.Id] = true
	}

	// Get embedded record IDs
	embeddingsCollection, err := app.FindCollectionByNameOrId(EmbeddingsCollectionName)
	if err != nil {
		// No embeddings collection yet, return all IDs
		result := make([]string, 0, len(allIds))
		for id := range allIds {
			result = append(result, id)
		}
		return result, nil
	}

	embeddedRecords, err := app.FindRecordsByFilter(
		embeddingsCollection.Id,
		"collection_id = {:collectionId} && field_name = {:fieldName}",
		"",
		0,
		0,
		map[string]any{
			"collectionId": collection.Id,
			"fieldName":    fieldName,
		},
	)
	if err != nil {
		embeddedRecords = []*Record{}
	}

	// Remove already embedded IDs
	for _, embRecord := range embeddedRecords {
		recordId := embRecord.GetString("record_id")
		delete(allIds, recordId)
	}

	// Return pending IDs
	result := make([]string, 0, len(allIds))
	for id := range allIds {
		result = append(result, id)
	}
	return result, nil
}

