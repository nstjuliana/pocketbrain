<script>
    import { createEventDispatcher } from "svelte";
    import { slide } from "svelte/transition";
    import ApiClient from "@/utils/ApiClient";
    import { addSuccessToast, addErrorToast } from "@/stores/toasts";
    import tooltip from "@/actions/tooltip";
    import OverlayPanel from "@/components/base/OverlayPanel.svelte";
    import Field from "@/components/base/Field.svelte";

    const dispatch = createEventDispatcher();

    export let collection;

    let panel;
    let selectedField = "";
    let isGenerating = false;
    let result = null;
    let stats = null;
    let isLoadingStats = false;

    // Get embeddable fields from collection (text and editor types)
    $: embeddableFields = (collection?.fields || []).filter((f) => {
        return (f.type === "text" || f.type === "editor") && f.embeddable;
    });

    $: hasEmbeddableFields = embeddableFields.length > 0;

    // Auto-select first embeddable field
    $: if (embeddableFields.length > 0 && !selectedField) {
        selectedField = embeddableFields[0].name;
    }

    // Load stats when field changes
    $: if (selectedField && collection?.id) {
        loadStats();
    }

    async function loadStats() {
        if (!selectedField || !collection?.id) return;
        
        isLoadingStats = true;
        stats = null;
        
        try {
            stats = await ApiClient.send(`/api/ai/embedding-stats?collectionId=${collection.id}&fieldName=${selectedField}`, {
                method: "GET",
            });
        } catch (err) {
            console.error("Failed to load embedding stats:", err);
        }
        
        isLoadingStats = false;
    }

    export function show() {
        result = null;
        stats = null;
        selectedField = embeddableFields.length > 0 ? embeddableFields[0].name : "";
        if (selectedField) {
            loadStats();
        }
        return panel?.show();
    }

    export function hide() {
        return panel?.hide();
    }

    async function generateEmbeddings() {
        if (isGenerating || !collection?.id || !selectedField) return;

        isGenerating = true;
        result = null;

        try {
            result = await ApiClient.send("/api/ai/generate-embeddings", {
                method: "POST",
                body: {
                    collectionId: collection.id,
                    fieldName: selectedField,
                },
            });

            if (result.generated > 0) {
                addSuccessToast(`Generated embeddings for ${result.generated} record${result.generated !== 1 ? "s" : ""}`);
                dispatch("generated", result);
                // Reload stats after generation
                loadStats();
            } else if (result.skipped > 0) {
                addSuccessToast(`All ${result.skipped} records already have embeddings or are empty`);
            } else {
                addErrorToast("No embeddings were generated");
            }
        } catch (err) {
            ApiClient.error(err);
            result = { error: err?.data?.message || err?.message || "Failed to generate embeddings" };
        }

        isGenerating = false;
    }

    function formatNumber(n) {
        if (n === null || n === undefined || n === "" || isNaN(n)) return "0";
        return Number(n).toLocaleString();
    }
</script>

<OverlayPanel bind:this={panel} class="embeddings-panel overlay-panel-lg" popup on:hide on:show>
    <svelte:fragment slot="header">
        <h4>
            <i class="ri-bubble-chart-line" aria-hidden="true" />
            <span class="txt">Generate Vector Embeddings</span>
        </h4>
    </svelte:fragment>

    <div class="content">
        {#if !hasEmbeddableFields}
            <div class="alert alert-warning">
                <i class="ri-error-warning-line" />
                <div>
                    <strong>No embeddable fields found</strong>
                    <p class="txt-sm m-t-5 m-b-0">
                        To generate embeddings, first enable "Vector embeddings" on a text field in the collection schema.
                    </p>
                </div>
            </div>
        {:else}
            <p class="txt-hint m-b-base">
                Generate vector embeddings for text fields in
                <strong>{collection?.name}</strong> to enable similarity search.
            </p>

            <!-- Field Selection -->
            <Field class="form-field m-b-base" name="field" let:uniqueId>
                <label for={uniqueId}>
                    Select field
                    <i
                        class="ri-information-line link-hint"
                        use:tooltip={{
                            text: "Choose which embeddable text field to generate embeddings for",
                            position: "top",
                        }}
                    />
                </label>
                <select id={uniqueId} bind:value={selectedField} disabled={isGenerating}>
                    {#each embeddableFields as field}
                        <option value={field.name}>{field.name}</option>
                    {/each}
                </select>
            </Field>

            <!-- Stats -->
            {#if stats}
                <div class="stats-section m-b-base" transition:slide={{ duration: 150 }}>
                    <div class="stats-grid">
                        <div class="stat-item">
                            <div class="stat-value">{formatNumber(stats.totalRecords)}</div>
                            <div class="stat-label">Total Records</div>
                        </div>
                        <div class="stat-item embedded">
                            <div class="stat-value">{formatNumber(stats.embeddedRecords)}</div>
                            <div class="stat-label">With Embeddings</div>
                        </div>
                        <div class="stat-item pending">
                            <div class="stat-value">{formatNumber(stats.notEmbeddedRecords)}</div>
                            <div class="stat-label">Pending</div>
                        </div>
                    </div>
                    
                    {#if stats.embeddedRecords > 0}
                        <div class="progress-bar m-t-sm">
                            <div 
                                class="progress-fill" 
                                style="width: {(stats.embeddedRecords / stats.totalRecords) * 100}%"
                            />
                        </div>
                        <p class="txt-hint txt-sm txt-center m-t-5">
                            {Math.round((stats.embeddedRecords / stats.totalRecords) * 100)}% complete
                        </p>
                    {/if}
                </div>
            {:else if isLoadingStats}
                <div class="stats-loading m-b-base">
                    <i class="ri-loader-4-line" />
                    Loading stats...
                </div>
            {/if}

            <!-- Info -->
            <div class="info-section m-b-base">
                <p class="txt-hint txt-sm">
                    <i class="ri-information-line" />
                    Embeddings are generated using OpenAI's embedding model. Records with existing embeddings will be updated.
                </p>
            </div>

            {#if result}
                <div class="result m-t-base" transition:slide={{ duration: 150 }}>
                    {#if result.error}
                        <div class="alert alert-danger">
                            <i class="ri-error-warning-line" />
                            {result.error}
                        </div>
                    {:else}
                        <div class="alert alert-success">
                            <i class="ri-check-line" />
                            <div class="result-content">
                                <strong>{formatNumber(result.generated)}</strong> embedding{result.generated !== 1 ? "s" : ""} generated
                                {#if result.skipped > 0}
                                    <span class="txt-hint">
                                        ({formatNumber(result.skipped)} skipped - empty or already embedded)
                                    </span>
                                {/if}
                            </div>
                        </div>
                        {#if result.errors && result.errors.length > 0}
                            <div class="error-details m-t-sm">
                                <p class="txt-hint txt-sm m-b-5">Errors:</p>
                                <ul class="txt-sm">
                                    {#each result.errors as error}
                                        <li class="txt-danger">{error}</li>
                                    {/each}
                                </ul>
                            </div>
                        {/if}
                    {/if}
                </div>
            {/if}
        {/if}
    </div>

    <svelte:fragment slot="footer">
        <button type="button" class="btn btn-transparent" disabled={isGenerating} on:click={() => hide()}>
            <span class="txt">Close</span>
        </button>
        {#if hasEmbeddableFields}
            <button
                type="button"
                class="btn btn-expanded"
                class:btn-loading={isGenerating}
                disabled={isGenerating || !selectedField}
                on:click={() => generateEmbeddings()}
            >
                <i class="ri-bubble-chart-line" aria-hidden="true" />
                <span class="txt">
                    {#if stats?.notEmbeddedRecords > 0}
                        Generate {formatNumber(stats.notEmbeddedRecords)} Embedding{stats.notEmbeddedRecords !== 1 ? "s" : ""}
                    {:else}
                        Generate Embeddings
                    {/if}
                </span>
            </button>
        {/if}
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

    /* Stats section */
    .stats-section {
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
        padding: 16px;
    }

    .stats-grid {
        display: grid;
        grid-template-columns: repeat(3, 1fr);
        gap: 12px;
    }

    .stat-item {
        text-align: center;
        padding: 8px;
        background: var(--baseColor);
        border-radius: var(--baseRadius);
    }

    .stat-value {
        font-size: 1.5em;
        font-weight: 700;
        color: var(--txtPrimaryColor);
    }

    .stat-label {
        font-size: 0.75em;
        color: var(--txtHintColor);
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }

    .stat-item.embedded .stat-value {
        color: var(--successColor);
    }

    .stat-item.pending .stat-value {
        color: var(--warningColor);
    }

    .progress-bar {
        height: 6px;
        background: var(--baseAlt2Color);
        border-radius: 3px;
        overflow: hidden;
    }

    .progress-fill {
        height: 100%;
        background: var(--successColor);
        transition: width 0.3s ease;
    }

    .stats-loading {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: 8px;
        padding: 20px;
        color: var(--txtHintColor);
    }

    .stats-loading i {
        animation: spin 1s linear infinite;
    }

    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }

    /* Info section */
    .info-section {
        background: var(--infoAltColor);
        padding: 12px;
        border-radius: var(--baseRadius);
    }

    .info-section p {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        margin: 0;
    }

    .info-section i {
        flex-shrink: 0;
        margin-top: 2px;
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

    .result-content {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
    }

    .error-details ul {
        margin: 0;
        padding-left: 20px;
    }
</style>

