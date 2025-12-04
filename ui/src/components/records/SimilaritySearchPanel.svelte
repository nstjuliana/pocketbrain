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
    let searchMode = "field"; // "field" or "record"
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

    // Check if collection has any text fields (for record-level search)
    $: hasTextFields = (collection?.fields || []).some(
        (f) => f.type === "text" || f.type === "editor" || f.type === "email" || f.type === "url"
    );

    // Can search if either field-level or record-level embeddings might exist
    $: canSearch = hasEmbeddableFields || hasTextFields;

    // Auto-select first embeddable field
    $: if (embeddableFields.length > 0 && !selectedField) {
        selectedField = embeddableFields[0].name;
    }

    export function show() {
        results = null;
        error = null;
        debug = null;
        searchText = "";
        // Default to field mode if available, otherwise record mode
        searchMode = hasEmbeddableFields ? "field" : "record";
        selectedField = embeddableFields.length > 0 ? embeddableFields[0].name : "";
        return panel?.show();
    }

    export function hide() {
        return panel?.hide();
    }

    async function search() {
        if (isSearching || !collection?.id || !searchText.trim()) return;
        if (searchMode === "field" && !selectedField) return;

        isSearching = true;
        results = null;
        debug = null;
        error = null;

        try {
            const body = {
                collectionId: collection.id,
                mode: searchMode,
                text: searchText.trim(),
                limit: limit,
            };

            if (searchMode === "field") {
                body.fieldName = selectedField;
            }

            const response = await ApiClient.send("/api/ai/find-similar", {
                method: "POST",
                body,
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

    function viewRecord(recordId) {
        // Close panel and dispatch event to open record
        hide();
        dispatch("viewRecord", { recordId });
    }

    function toggleExpand(index) {
        results = results.map((r, i) => {
            if (i === index) {
                return { ...r, expanded: !r.expanded };
            }
            return r;
        });
    }

    function getFullRecordText(record) {
        if (!record) return "";
        
        const textFields = (collection?.fields || []).filter(
            (f) => f.type === "text" || f.type === "editor"
        );
        
        const parts = [];
        for (const field of textFields) {
            const value = record[field.name];
            if (value) {
                const text = String(value).replace(/<[^>]*>/g, '').trim();
                if (text) {
                    parts.push(`**${field.name}:**\n${text}`);
                }
            }
        }
        return parts.join("\n\n");
    }

    function getRecordPreview(record) {
        if (!record) return "Record not found";
        
        // For record-level mode, show a combined preview from multiple fields
        if (searchMode === "record") {
            const textFields = (collection?.fields || []).filter(
                (f) => f.type === "text" || f.type === "editor"
            );
            const parts = [];
            for (const field of textFields) {
                const value = record[field.name];
                if (value) {
                    const text = String(value).replace(/<[^>]*>/g, '').trim();
                    if (text) {
                        // Show field name and truncated value
                        const truncated = text.length > 50 ? text.substring(0, 50) + "..." : text;
                        parts.push(`${field.name}: ${truncated}`);
                    }
                }
                if (parts.length >= 2) break; // Show max 2 fields
            }
            return parts.length > 0 ? parts.join(" | ") : record.id;
        }
        
        // For field-level mode, show the specific field
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
        {#if !canSearch}
            <div class="alert alert-warning">
                <i class="ri-error-warning-line" />
                <div>
                    <strong>No searchable embeddings</strong>
                    <p class="txt-sm m-t-5 m-b-0">
                        First generate embeddings using the Embeddings panel (field-level or entire record).
                    </p>
                </div>
            </div>
        {:else}
            <p class="txt-hint m-b-base">
                Find records in <strong>{collection?.name}</strong> similar to your search text using vector similarity.
            </p>

            <!-- Search Mode -->
            <Field class="form-field m-b-base" name="searchMode" let:uniqueId>
                <label for={uniqueId}>
                    Search mode
                    <i
                        class="ri-information-line link-hint"
                        use:tooltip={{
                            text: "Field: Search by individual field embeddings. Record: Search by whole-record embeddings.",
                            position: "top",
                        }}
                    />
                </label>
                <div class="mode-selector">
                    <button 
                        type="button" 
                        class="mode-btn" 
                        class:active={searchMode === "field"}
                        disabled={isSearching || !hasEmbeddableFields}
                        on:click={() => searchMode = "field"}
                    >
                        <i class="ri-text-spacing" />
                        <span>Field</span>
                    </button>
                    <button 
                        type="button" 
                        class="mode-btn" 
                        class:active={searchMode === "record"}
                        disabled={isSearching}
                        on:click={() => searchMode = "record"}
                    >
                        <i class="ri-file-list-3-line" />
                        <span>Record</span>
                    </button>
                </div>
            </Field>

            <!-- Field Selection (only for field mode) -->
            <div class="grid grid-sm m-b-base">
                {#if searchMode === "field"}
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
                {:else}
                    <div class="col-sm-12">
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
                {/if}
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
                            <p><strong>Cache hit:</strong> <span class={debug.cacheHit ? "txt-success" : "txt-hint"}>{debug.cacheHit ? "Yes ⚡" : "No (loaded from DB)"}</span></p>
                            {#if debug.cacheSkipped}
                                <p class="txt-warning"><strong>Cache skipped:</strong> Too many records (>{50000})</p>
                            {/if}
                            {#if debug.cacheStats}
                                <div class="cache-stats">
                                    <p><strong>Cache:</strong> 
                                        {debug.cacheStats.entriesCount} entries, 
                                        {debug.cacheStats.memoryUsedMB.toFixed(1)}MB / {debug.cacheStats.memoryBudgetMB}MB 
                                        ({debug.cacheStats.memoryUsagePercent.toFixed(0)}%)
                                    </p>
                                    <div class="memory-bar">
                                        <div 
                                            class="memory-fill" 
                                            class:warning={debug.cacheStats.memoryUsagePercent > 80}
                                            style="width: {Math.min(debug.cacheStats.memoryUsagePercent, 100)}%"
                                        ></div>
                                    </div>
                                </div>
                            {/if}
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
                                            <div class="result-actions">
                                                <span class="similarity-badge" class:high={result.similarity > 0.8} class:medium={result.similarity > 0.5 && result.similarity <= 0.8}>
                                                    {formatSimilarity(result.similarity)}
                                                </span>
                                                <button
                                                    type="button"
                                                    class="btn btn-xs btn-outline"
                                                    title="View record"
                                                    on:click={() => viewRecord(result.recordId)}
                                                >
                                                    <i class="ri-external-link-line" />
                                                </button>
                                            </div>
                                        </div>
                                        <div class="result-preview">
                                            {getRecordPreview(result.record)}
                                        </div>
                                        {#if result.expanded}
                                            <div class="result-full-text" transition:slide={{ duration: 150 }}>
                                                {getFullRecordText(result.record)}
                                            </div>
                                        {/if}
                                        <button
                                            type="button"
                                            class="btn-expand"
                                            on:click={() => toggleExpand(index)}
                                        >
                                            {result.expanded ? "Show less" : "Show more"}
                                            <i class={result.expanded ? "ri-arrow-up-s-line" : "ri-arrow-down-s-line"} />
                                        </button>
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

    /* Cache stats */
    .cache-stats {
        margin-top: 8px;
        padding-top: 8px;
        border-top: 1px solid var(--baseAlt1Color);
    }

    .memory-bar {
        height: 6px;
        background: var(--baseAlt1Color);
        border-radius: 3px;
        overflow: hidden;
        margin-top: 4px;
    }

    .memory-fill {
        height: 100%;
        background: var(--successColor);
        border-radius: 3px;
        transition: width 0.3s ease;
    }

    .memory-fill.warning {
        background: var(--warningColor);
    }

    /* Mode selector */
    .mode-selector {
        display: flex;
        gap: 8px;
    }

    .mode-btn {
        flex: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 6px;
        padding: 10px 14px;
        border: 2px solid var(--baseAlt2Color);
        border-radius: var(--baseRadius);
        background: var(--baseColor);
        color: var(--txtHintColor);
        cursor: pointer;
        transition: all 0.2s ease;
        font-size: 0.9em;
    }

    .mode-btn:hover:not(:disabled) {
        border-color: var(--primaryColor);
        color: var(--txtPrimaryColor);
    }

    .mode-btn.active {
        border-color: var(--primaryColor);
        background: var(--primaryAltColor);
        color: var(--primaryColor);
    }

    .mode-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .mode-btn i {
        font-size: 1.1em;
    }

    /* Result actions */
    .result-actions {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .result-actions .btn-xs {
        padding: 4px 8px;
        font-size: 0.8em;
    }

    .result-actions .btn-xs i {
        font-size: 1em;
    }

    /* Expand button */
    .btn-expand {
        display: flex;
        align-items: center;
        gap: 4px;
        margin-top: 8px;
        padding: 4px 8px;
        border: none;
        background: transparent;
        color: var(--primaryColor);
        font-size: 0.8em;
        cursor: pointer;
        border-radius: var(--baseRadius);
    }

    .btn-expand:hover {
        background: var(--primaryAltColor);
    }

    /* Full text display */
    .result-full-text {
        margin-top: 12px;
        padding: 12px;
        background: var(--baseAlt2Color);
        border-radius: var(--baseRadius);
        font-size: 0.85em;
        line-height: 1.5;
        white-space: pre-wrap;
        word-break: break-word;
        max-height: 300px;
        overflow-y: auto;
    }
</style>

