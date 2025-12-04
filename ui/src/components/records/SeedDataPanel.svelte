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

    // Hybrid threshold - matches backend constant
    const HYBRID_THRESHOLD = 20;
    const MAX_COUNT = 1000000;

    let panel;
    let count = 100;
    let description = "";
    let isGenerating = false;
    let result = null;

    // Quick presets for count selection
    const countPresets = [10, 100, 1000, 10000, 100000, 1000000];

    // Safe count value (handles null/empty input)
    $: safeCount = (count && !isNaN(count)) ? Math.max(1, Math.min(MAX_COUNT, parseInt(count))) : 1;

    // Determine if fast mode (hybrid) will be used
    $: isFastMode = safeCount > HYBRID_THRESHOLD;

    // Estimated time based on count and mode
    $: estimatedTime = getEstimatedTime(safeCount);

    function getEstimatedTime(n) {
        if (!n || isNaN(n)) return "~2s";
        if (n <= HYBRID_THRESHOLD) {
            // Pure AI mode: ~2-5 seconds
            return `~${Math.max(2, Math.ceil(n / 5))}s (AI)`;
        } else {
            // Hybrid mode: much faster
            if (n <= 100) return "~2-3s";
            if (n <= 1000) return "~3-5s";
            if (n <= 10000) return "~5-15s";
            if (n <= 100000) return "~30s-2min";
            return "~2-5min";
        }
    }

    // Safe number formatting
    function formatNumber(n) {
        if (n === null || n === undefined || n === "" || isNaN(n)) return "0";
        return Number(n).toLocaleString();
    }

    // Fields that will be generated (for display)
    $: generatableFields = (collection?.fields || []).filter((f) => {
        // Skip system fields
        if (f.name === "id" || f.name === "created" || f.name === "updated") {
            return false;
        }
        // Skip relation, file, autodate, password fields
        const skipTypes = ["relation", "file", "autodate"];
        if (skipTypes.includes(f.type)) {
            return false;
        }
        // Skip password for auth collections
        if (f.name === "password") {
            return false;
        }
        return true;
    });

    $: skippedFields = (collection?.fields || []).filter((f) => {
        if (f.name === "id" || f.name === "created" || f.name === "updated") {
            return false;
        }
        const skipTypes = ["relation", "file", "autodate"];
        if (skipTypes.includes(f.type) || f.name === "password") {
            return true;
        }
        return false;
    });

    // Check for required relation fields - these will cause validation errors
    $: requiredRelationFields = (collection?.fields || []).filter((f) => {
        return f.type === "relation" && f.required;
    });

    $: hasRequiredRelations = requiredRelationFields.length > 0;

    export function show() {
        result = null;
        count = 100;
        description = "";
        return panel?.show();
    }

    export function hide() {
        return panel?.hide();
    }

    async function generateSeedData() {
        if (isGenerating || !collection?.id || !safeCount) return;

        isGenerating = true;
        result = null;

        try {
            result = await ApiClient.ai.generateSeedData({
                collectionId: collection.id,
                count: safeCount,
                description: description || undefined,
            });

            if (result.created > 0) {
                addSuccessToast(`Created ${result.created} record${result.created !== 1 ? "s" : ""}`);
                dispatch("generated", result);
            } else {
                addErrorToast("No records were created");
            }
        } catch (err) {
            ApiClient.error(err);
            result = { error: err?.data?.message || err?.message || "Failed to generate seed data" };
        }

        isGenerating = false;
    }

    function handleKeyPress(e) {
        if (e.key === "Enter" && !e.shiftKey && !isGenerating) {
            e.preventDefault();
            generateSeedData();
        }
    }

    function setCount(preset) {
        count = preset;
    }
</script>

<OverlayPanel bind:this={panel} class="seed-data-panel overlay-panel-lg" popup on:hide on:show>
    <svelte:fragment slot="header">
        <h4>
            <i class="ri-seedling-line" aria-hidden="true" />
            <span class="txt">Generate Seed Data</span>
            {#if isFastMode}
                <span class="fast-mode-badge" transition:slide={{ duration: 150, axis: "x" }}>
                    <i class="ri-flashlight-line" aria-hidden="true" />
                    Fast Mode
                </span>
            {/if}
        </h4>
    </svelte:fragment>

    <div class="content">
        <p class="txt-hint m-b-base">
            Use AI to generate realistic sample records for
            <strong>{collection?.name}</strong>.
        </p>

        <!-- Count Selection -->
        <div class="count-section m-b-base">
            <div class="count-header">
                <label class="count-label">Number of records</label>
                <span class="estimated-time" class:fast={isFastMode}>
                    <i class={isFastMode ? "ri-flashlight-line" : "ri-time-line"} aria-hidden="true" />
                    {estimatedTime}
                </span>
            </div>
            
            <div class="count-presets">
                {#each countPresets as preset}
                    <button
                        type="button"
                        class="preset-btn"
                        class:active={count === preset}
                        class:fast={preset > HYBRID_THRESHOLD}
                        disabled={isGenerating}
                        on:click={() => setCount(preset)}
                    >
                        {formatNumber(preset)}
                    </button>
                {/each}
            </div>

            <div class="count-input-row">
                <input
                    type="range"
                    class="count-slider"
                    bind:value={count}
                    min="1"
                    max={MAX_COUNT}
                    step="1"
                    disabled={isGenerating}
                />
                <input
                    type="number"
                    class="count-input"
                    bind:value={count}
                    min="1"
                    max={MAX_COUNT}
                    required
                    disabled={isGenerating}
                />
            </div>

            {#if isFastMode}
                <p class="mode-hint fast" transition:slide={{ duration: 150 }}>
                    <i class="ri-information-line" aria-hidden="true" />
                    Fast mode uses AI archetypes + procedural generation for speed
                </p>
            {:else}
                <p class="mode-hint" transition:slide={{ duration: 150 }}>
                    <i class="ri-information-line" aria-hidden="true" />
                    Pure AI mode generates each record individually
                </p>
            {/if}
        </div>

        <!-- Description -->
        <Field class="form-field m-b-base" name="description" let:uniqueId>
            <label for={uniqueId}>
                Description
                <i
                    class="ri-information-line link-hint"
                    use:tooltip={{
                        text: "Optional context to make the generated data more relevant (e.g., 'blog posts about technology' or 'products for an online bookstore')",
                        position: "top",
                    }}
                />
            </label>
            <textarea
                id={uniqueId}
                bind:value={description}
                placeholder="Optional: describe the type of data you want..."
                rows="2"
                disabled={isGenerating}
                on:keydown={handleKeyPress}
            />
        </Field>

        {#if generatableFields.length > 0}
            <div class="fields-preview m-t-base" transition:slide={{ duration: 150 }}>
                <p class="section-title txt-hint">
                    <i class="ri-database-2-line" aria-hidden="true" />
                    Fields to generate ({generatableFields.length})
                </p>
                <div class="field-tags">
                    {#each generatableFields as field}
                        <span class="label" title={field.type}>
                            {field.name}
                            <small class="txt-hint">({field.type})</small>
                        </span>
                    {/each}
                </div>
            </div>
        {:else}
            <div class="alert alert-warning m-t-base">
                <i class="ri-error-warning-line" />
                No fields suitable for seed data generation found.
            </div>
        {/if}

        {#if hasRequiredRelations}
            <div class="alert alert-warning m-t-base" transition:slide={{ duration: 150 }}>
                <i class="ri-error-warning-line" />
                <div>
                    <strong>Required relation field{requiredRelationFields.length > 1 ? "s" : ""}:</strong>
                    {requiredRelationFields.map((f) => f.name).join(", ")}
                    <p class="txt-sm m-t-5 m-b-0">
                        Relation fields can't be auto-generated. Records will fail validation unless you 
                        make these fields optional or manually add related records first.
                    </p>
                </div>
            </div>
        {/if}

        {#if skippedFields.length > 0}
            {@const otherSkipped = skippedFields.filter(f => !f.required || f.type !== "relation")}
            {#if otherSkipped.length > 0}
                <div class="skipped-fields m-t-sm" transition:slide={{ duration: 150 }}>
                    <p class="txt-hint txt-sm">
                        <i class="ri-skip-forward-line" aria-hidden="true" />
                        Skipped: {otherSkipped.map((f) => f.name).join(", ")}
                        <i
                            class="ri-information-line link-hint"
                            use:tooltip={{
                                text: "Relation, file, autodate, and password fields cannot be auto-generated",
                                position: "top",
                            }}
                        />
                    </p>
                </div>
            {/if}
        {/if}

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
                            <strong>{formatNumber(result.created)}</strong> record{result.created !== 1 ? "s" : ""} created
                            {#if result.mode === "hybrid"}
                                <span class="mode-tag fast">
                                    <i class="ri-flashlight-line" aria-hidden="true" />
                                    Fast
                                </span>
                            {/if}
                            {#if result.skipped > 0}
                                <span class="txt-hint">
                                    ({formatNumber(result.skipped)} skipped due to validation errors)
                                </span>
                            {/if}
                            {#if result.total < safeCount && result.skipped === 0}
                                <span class="txt-hint">
                                    (AI returned {formatNumber(result.total)} instead of {formatNumber(safeCount)})
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
    </div>

    <svelte:fragment slot="footer">
        <button type="button" class="btn btn-transparent" disabled={isGenerating} on:click={() => hide()}>
            <span class="txt">Close</span>
        </button>
        <button
            type="button"
            class="btn btn-expanded"
            class:btn-loading={isGenerating}
            class:btn-success={isFastMode}
            disabled={isGenerating || generatableFields.length === 0}
            on:click={() => generateSeedData()}
        >
            {#if isFastMode}
                <i class="ri-flashlight-line" aria-hidden="true" />
            {:else}
                <i class="ri-magic-line" aria-hidden="true" />
            {/if}
            <span class="txt">Generate {formatNumber(safeCount)} Record{safeCount !== 1 ? "s" : ""}</span>
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
        color: var(--successColor);
    }

    /* Fast mode badge in header */
    .fast-mode-badge {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        background: linear-gradient(135deg, var(--successColor), #10b981);
        color: white;
        border-radius: 12px;
        font-size: 0.7em;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }

    .fast-mode-badge i {
        font-size: 1em;
        color: white;
    }

    /* Count section */
    .count-section {
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
        padding: 16px;
    }

    .count-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 12px;
    }

    .count-label {
        font-weight: 600;
        font-size: 0.9em;
    }

    .estimated-time {
        display: flex;
        align-items: center;
        gap: 4px;
        font-size: 0.85em;
        color: var(--txtHintColor);
        padding: 4px 8px;
        background: var(--baseAlt2Color);
        border-radius: 8px;
    }

    .estimated-time.fast {
        background: rgba(16, 185, 129, 0.15);
        color: var(--successColor);
    }

    .estimated-time i {
        font-size: 1em;
    }

    /* Count presets */
    .count-presets {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        margin-bottom: 12px;
    }

    .preset-btn {
        padding: 6px 12px;
        border: 1px solid var(--borderColor);
        border-radius: 6px;
        background: var(--baseColor);
        color: var(--txtPrimaryColor);
        font-size: 0.85em;
        font-weight: 500;
        cursor: pointer;
        transition: all 0.15s ease;
    }

    .preset-btn:hover:not(:disabled) {
        border-color: var(--primaryColor);
        background: var(--primaryAltColor);
    }

    .preset-btn.active {
        border-color: var(--primaryColor);
        background: var(--primaryColor);
        color: var(--primaryFgColor);
    }

    .preset-btn.fast:not(.active) {
        border-color: rgba(16, 185, 129, 0.3);
    }

    .preset-btn.fast:hover:not(:disabled):not(.active) {
        border-color: var(--successColor);
        background: rgba(16, 185, 129, 0.1);
    }

    .preset-btn.fast.active {
        background: var(--successColor);
        border-color: var(--successColor);
    }

    .preset-btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    /* Count input row */
    .count-input-row {
        display: flex;
        align-items: center;
        gap: 12px;
        margin-bottom: 8px;
    }

    .count-slider {
        flex: 1;
        height: 6px;
        border-radius: 3px;
        background: var(--borderColor);
        appearance: none;
        cursor: pointer;
    }

    .count-slider::-webkit-slider-thumb {
        appearance: none;
        width: 18px;
        height: 18px;
        border-radius: 50%;
        background: var(--primaryColor);
        cursor: pointer;
        border: 2px solid var(--baseColor);
        box-shadow: 0 2px 4px rgba(0, 0, 0, 0.2);
    }

    .count-slider::-moz-range-thumb {
        width: 18px;
        height: 18px;
        border-radius: 50%;
        background: var(--primaryColor);
        cursor: pointer;
        border: 2px solid var(--baseColor);
        box-shadow: 0 2px 4px rgba(0, 0, 0, 0.2);
    }

    .count-input {
        width: 100px;
        padding: 8px 12px;
        border: 1px solid var(--borderColor);
        border-radius: var(--baseRadius);
        font-size: 0.95em;
        text-align: center;
        font-weight: 600;
    }

    .count-input:focus {
        outline: none;
        border-color: var(--primaryColor);
    }

    /* Mode hint */
    .mode-hint {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.8em;
        color: var(--txtHintColor);
        margin: 0;
    }

    .mode-hint.fast {
        color: var(--successColor);
    }

    .mode-hint i {
        font-size: 1em;
    }

    /* Fields preview */
    .fields-preview {
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
        padding: 12px;
    }

    .section-title {
        display: flex;
        align-items: center;
        gap: 6px;
        margin-bottom: 8px;
        font-size: 0.85em;
    }

    .field-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
    }

    .field-tags .label {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        padding: 4px 8px;
        background: var(--baseAlt2Color);
        border-radius: var(--baseRadius);
        font-size: 0.85em;
    }

    .field-tags .label small {
        font-size: 0.8em;
        opacity: 0.7;
    }

    .skipped-fields {
        font-size: 0.85em;
    }

    .skipped-fields p {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-wrap: wrap;
    }

    .error-details ul {
        margin: 0;
        padding-left: 20px;
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

    /* Result content */
    .result-content {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
    }

    .mode-tag {
        display: inline-flex;
        align-items: center;
        gap: 3px;
        padding: 2px 6px;
        border-radius: 4px;
        font-size: 0.75em;
        font-weight: 600;
        text-transform: uppercase;
    }

    .mode-tag.fast {
        background: rgba(16, 185, 129, 0.15);
        color: var(--successColor);
    }

    .mode-tag i {
        font-size: 0.9em;
    }

    textarea {
        resize: vertical;
        min-height: 60px;
    }

    /* Success button variant */
    :global(.btn-success) {
        background: var(--successColor) !important;
        border-color: var(--successColor) !important;
    }

    :global(.btn-success:hover:not(:disabled)) {
        background: #059669 !important;
        border-color: #059669 !important;
    }
</style>

