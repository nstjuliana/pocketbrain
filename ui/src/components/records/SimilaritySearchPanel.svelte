<script>
    import { createEventDispatcher } from "svelte";
    import { slide } from "svelte/transition";
    import ApiClient from "@/utils/ApiClient";
    import { addErrorToast } from "@/stores/toasts";
    import tooltip from "@/actions/tooltip";
    import OverlayPanel from "@/components/base/OverlayPanel.svelte";
    import Field from "@/components/base/Field.svelte";

    const dispatch = createEventDispatcher();

    export let collection;

    let panel;
    let selectedField = "";
    let searchText = "";
    let limit = 10;
    let isSearching = false;
    let results = null;
    let debug = null;
    let error = null;

    // Get embeddable fields from collection (text and editor types)
    $: embeddableFields = (collection?.fields || []).filter((f) => {
        return (f.type === "text" || f.type === "editor") && f.embeddable;
    });

    $: hasEmbeddableFields = embeddableFields.length > 0;

    // Auto-select first embeddable field
    $: if (embeddableFields.length > 0 && !selectedField) {
        selectedField = embeddableFields[0].name;
    }

    export function show() {
        results = null;
        error = null;
        searchText = "";
        selectedField = embeddableFields.length > 0 ? embeddableFields[0].name : "";
        return panel?.show();
    }

    export function hide() {
        return panel?.hide();
    }

    async function search() {
        if (isSearching || !collection?.id || !selectedField || !searchText.trim()) return;

        isSearching = true;
        results = null;
        debug = null;
        error = null;

        try {
            const response = await ApiClient.send("/api/ai/find-similar", {
                method: "POST",
                body: {
                    collectionId: collection.id,
                    fieldName: selectedField,
                    text: searchText.trim(),
                    limit: limit,
                },
            });

            results = response.results || [];
            debug = response.debug || null;
            
            console.log("Similarity search response:", response);
            
            // Fetch the actual records to show their data
            if (results.length > 0) {
                const recordsData = await Promise.all(
                    results.map(async (r) => {
                        try {
                            const record = await ApiClient.collection(collection.name).getOne(r.recordId);
                            return { ...r, record };
                        } catch (e) {
                            return { ...r, record: null };
                        }
                    })
                );
                results = recordsData;
            }
        } catch (err) {
            console.error("Similarity search error:", err);
            error = err?.data?.message || err?.message || "Failed to search";
            addErrorToast(error);
        }

        isSearching = false;
    }

    function handleKeyPress(e) {
        if (e.key === "Enter" && !e.shiftKey && !isSearching) {
            e.preventDefault();
            search();
        }
    }

    function formatSimilarity(score) {
        return (score * 100).toFixed(1) + "%";
    }

    function getRecordPreview(record) {
        if (!record) return "Record not found";
        
        // Try to get a meaningful preview
        const previewField = selectedField;
        const value = record[previewField];
        if (value) {
            // Strip HTML for editor fields
            const text = String(value).replace(/<[^>]*>/g, '');
            return text.length > 100 ? text.substring(0, 100) + "..." : text;
        }
        return record.id;
    }
</script>

<OverlayPanel bind:this={panel} class="similarity-search-panel overlay-panel-lg" popup on:hide on:show>
    <svelte:fragment slot="header">
        <h4>
            <i class="ri-search-eye-line" aria-hidden="true" />
            <span class="txt">Similarity Search</span>
        </h4>
    </svelte:fragment>

    <div class="content">
        {#if !hasEmbeddableFields}
            <div class="alert alert-warning">
                <i class="ri-error-warning-line" />
                <div>
                    <strong>No embeddable fields with embeddings</strong>
                    <p class="txt-sm m-t-5 m-b-0">
                        First enable "Vector embeddings" on a text field and generate embeddings.
                    </p>
                </div>
            </div>
        {:else}
            <p class="txt-hint m-b-base">
                Find records in <strong>{collection?.name}</strong> similar to your search text using vector similarity.
            </p>

            <!-- Field Selection -->
            <div class="grid grid-sm m-b-base">
                <div class="col-sm-8">
                    <Field class="form-field" name="field" let:uniqueId>
                        <label for={uniqueId}>Field to search</label>
                        <select id={uniqueId} bind:value={selectedField} disabled={isSearching}>
                            {#each embeddableFields as field}
                                <option value={field.name}>{field.name}</option>
                            {/each}
                        </select>
                    </Field>
                </div>
                <div class="col-sm-4">
                    <Field class="form-field" name="limit" let:uniqueId>
                        <label for={uniqueId}>Max results</label>
                        <input 
                            type="number" 
                            id={uniqueId} 
                            bind:value={limit} 
                            min="1" 
                            max="100"
                            disabled={isSearching}
                        />
                    </Field>
                </div>
            </div>

            <!-- Search Input -->
            <Field class="form-field m-b-base" name="searchText" let:uniqueId>
                <label for={uniqueId}>
                    Search text
                    <i
                        class="ri-information-line link-hint"
                        use:tooltip={{
                            text: "Enter text to find semantically similar records. The search uses vector embeddings, so it finds conceptually related content, not just keyword matches.",
                            position: "top",
                        }}
                    />
                </label>
                <textarea
                    id={uniqueId}
                    bind:value={searchText}
                    placeholder="Enter text to find similar records..."
                    rows="3"
                    disabled={isSearching}
                    on:keydown={handleKeyPress}
                />
            </Field>

            <!-- Search Button -->
            <button
                type="button"
                class="btn btn-expanded m-b-base"
                class:btn-loading={isSearching}
                disabled={isSearching || !searchText.trim()}
                on:click={search}
            >
                <i class="ri-search-line" aria-hidden="true" />
                <span class="txt">Search Similar</span>
            </button>

            <!-- Error -->
            {#if error}
                <div class="alert alert-danger m-b-base" transition:slide={{ duration: 150 }}>
                    <i class="ri-error-warning-line" />
                    <div>{error}</div>
                </div>
            {/if}

            <!-- Debug Info -->
            {#if debug}
                <div class="debug-section m-b-base" transition:slide={{ duration: 150 }}>
                    <details>
                        <summary class="txt-hint">Debug Info</summary>
                        <div class="debug-content">
                            <p><strong>Collection ID:</strong> {debug.collectionId}</p>
                            <p><strong>Field:</strong> {debug.fieldName}</p>
                            <p><strong>Query embedding length:</strong> {debug.queryEmbeddingLen}</p>
                            <p><strong>Stored embeddings found:</strong> {debug.storedEmbeddings}</p>
                            <p><strong>Successfully processed:</strong> {debug.processedCount}</p>
                            <p><strong>Errors:</strong> {debug.errorCount}</p>
                            {#if debug.errors && debug.errors.length > 0}
                                <div class="error-list">
                                    <p><strong>Error details:</strong></p>
                                    {#each debug.errors as errMsg}
                                        <p class="txt-danger txt-sm">{errMsg}</p>
                                    {/each}
                                </div>
                            {/if}
                        </div>
                    </details>
                </div>
            {/if}

            <!-- Results -->
            {#if results !== null}
                <div class="results-section" transition:slide={{ duration: 150 }}>
                    <h5 class="section-title">
                        <i class="ri-list-check" aria-hidden="true" />
                        Results ({results.length})
                    </h5>
                    
                    {#if results.length === 0}
                        <p class="txt-hint txt-center p-base">No similar records found.</p>
                    {:else}
                        <div class="results-list">
                            {#each results as result, index}
                                <div class="result-item">
                                    <div class="result-rank">#{index + 1}</div>
                                    <div class="result-content">
                                        <div class="result-header">
                                            <span class="result-id" title={result.recordId}>
                                                {result.recordId}
                                            </span>
                                            <span class="similarity-badge" class:high={result.similarity > 0.8} class:medium={result.similarity > 0.5 && result.similarity <= 0.8}>
                                                {formatSimilarity(result.similarity)}
                                            </span>
                                        </div>
                                        <div class="result-preview">
                                            {getRecordPreview(result.record)}
                                        </div>
                                    </div>
                                </div>
                            {/each}
                        </div>
                    {/if}
                </div>
            {/if}
        {/if}
    </div>

    <svelte:fragment slot="footer">
        <button type="button" class="btn btn-transparent" disabled={isSearching} on:click={() => hide()}>
            <span class="txt">Close</span>
        </button>
    </svelte:fragment>
</OverlayPanel>

<style>
    h4 {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    h4 i {
        font-size: 1.2em;
        color: var(--primaryColor);
    }

    textarea {
        resize: vertical;
        min-height: 80px;
    }

    /* Results section */
    .results-section {
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
        padding: 16px;
    }

    .section-title {
        display: flex;
        align-items: center;
        gap: 8px;
        margin: 0 0 12px 0;
        font-size: 0.95em;
        color: var(--txtPrimaryColor);
    }

    .section-title i {
        color: var(--primaryColor);
    }

    .results-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .result-item {
        display: flex;
        gap: 12px;
        background: var(--baseColor);
        border-radius: var(--baseRadius);
        padding: 12px;
        border: 1px solid var(--baseAlt2Color);
    }

    .result-rank {
        flex-shrink: 0;
        width: 32px;
        height: 32px;
        display: flex;
        align-items: center;
        justify-content: center;
        background: var(--baseAlt2Color);
        border-radius: 50%;
        font-weight: 600;
        font-size: 0.85em;
        color: var(--txtHintColor);
    }

    .result-content {
        flex: 1;
        min-width: 0;
    }

    .result-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        margin-bottom: 4px;
    }

    .result-id {
        font-family: monospace;
        font-size: 0.85em;
        color: var(--txtHintColor);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .similarity-badge {
        flex-shrink: 0;
        padding: 2px 8px;
        border-radius: 12px;
        font-size: 0.8em;
        font-weight: 600;
        background: var(--baseAlt2Color);
        color: var(--txtHintColor);
    }

    .similarity-badge.high {
        background: rgba(16, 185, 129, 0.15);
        color: var(--successColor);
    }

    .similarity-badge.medium {
        background: rgba(245, 158, 11, 0.15);
        color: var(--warningColor);
    }

    .result-preview {
        font-size: 0.9em;
        color: var(--txtPrimaryColor);
        line-height: 1.4;
    }

    /* Alerts */
    .alert {
        display: flex;
        align-items: flex-start;
        gap: 8px;
    }

    .alert i {
        font-size: 1.1em;
        flex-shrink: 0;
        margin-top: 2px;
    }

    .alert-warning {
        background: var(--warningAltColor);
        color: var(--txtPrimaryColor);
        padding: 12px;
        border-radius: var(--baseRadius);
    }

    .alert-warning strong {
        color: var(--warningColor);
    }

    /* Debug section */
    .debug-section {
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
        padding: 8px 12px;
    }

    .debug-section summary {
        cursor: pointer;
        font-size: 0.85em;
    }

    .debug-content {
        margin-top: 8px;
        padding: 8px;
        background: var(--baseAlt2Color);
        border-radius: var(--baseRadius);
        font-size: 0.85em;
    }

    .debug-content p {
        margin: 4px 0;
    }
</style>

