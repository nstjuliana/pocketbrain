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
    let count = 10;
    let description = "";
    let isGenerating = false;
    let result = null;

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

    export function show() {
        result = null;
        count = 10;
        description = "";
        return panel?.show();
    }

    export function hide() {
        return panel?.hide();
    }

    async function generateSeedData() {
        if (isGenerating || !collection?.id) return;

        isGenerating = true;
        result = null;

        try {
            result = await ApiClient.ai.generateSeedData({
                collectionId: collection.id,
                count: count,
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
</script>

<OverlayPanel bind:this={panel} class="seed-data-panel overlay-panel-lg" popup on:hide on:show>
    <svelte:fragment slot="header">
        <h4>
            <i class="ri-seedling-line" aria-hidden="true" />
            <span class="txt">Generate Seed Data</span>
        </h4>
    </svelte:fragment>

    <div class="content">
        <p class="txt-hint m-b-base">
            Use AI to generate realistic sample records for
            <strong>{collection?.name}</strong>.
        </p>

        <div class="grid">
            <div class="col-sm-4">
                <Field class="form-field required" name="count" let:uniqueId>
                    <label for={uniqueId}>Number of records</label>
                    <input
                        type="number"
                        id={uniqueId}
                        bind:value={count}
                        min="1"
                        max="50"
                        required
                        disabled={isGenerating}
                    />
                    <div class="help-block">Max: 50</div>
                </Field>
            </div>

            <div class="col-sm-8">
                <Field class="form-field" name="description" let:uniqueId>
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
            </div>
        </div>

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

        {#if skippedFields.length > 0}
            <div class="skipped-fields m-t-sm" transition:slide={{ duration: 150 }}>
                <p class="txt-hint txt-sm">
                    <i class="ri-skip-forward-line" aria-hidden="true" />
                    Skipped: {skippedFields.map((f) => f.name).join(", ")}
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
                        <strong>{result.created}</strong> record{result.created !== 1 ? "s" : ""} created
                        {#if result.skipped > 0}
                            <span class="txt-hint">
                                ({result.skipped} skipped due to validation errors)
                            </span>
                        {/if}
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
            disabled={isGenerating || generatableFields.length === 0}
            on:click={() => generateSeedData()}
        >
            <i class="ri-magic-line" aria-hidden="true" />
            <span class="txt">Generate {count} Record{count !== 1 ? "s" : ""}</span>
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

    .alert {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .alert i {
        font-size: 1.1em;
    }

    textarea {
        resize: vertical;
        min-height: 60px;
    }
</style>

