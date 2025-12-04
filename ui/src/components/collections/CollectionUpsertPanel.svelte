<script>
    import { createEventDispatcher, tick } from "svelte";
    import { scale, slide } from "svelte/transition";
    import ApiClient from "@/utils/ApiClient";
    import CommonHelper from "@/utils/CommonHelper";
    import { confirm } from "@/stores/confirmation";
    import { errors, removeError, setErrors } from "@/stores/errors";
    import { addSuccessToast, addInfoToast, removeAllToasts } from "@/stores/toasts";
    import {
        addCollection,
        removeCollection,
        scaffolds,
        activeCollection,
        refreshScaffolds,
    } from "@/stores/collections";
    import tooltip from "@/actions/tooltip";
    import Field from "@/components/base/Field.svelte";
    import OverlayPanel from "@/components/base/OverlayPanel.svelte";
    import Toggler from "@/components/base/Toggler.svelte";
    import CollectionAuthOptionsTab from "@/components/collections/CollectionAuthOptionsTab.svelte";
    import CollectionFieldsTab from "@/components/collections/CollectionFieldsTab.svelte";
    import CollectionQueryTab from "@/components/collections/CollectionQueryTab.svelte";
    import CollectionRulesTab from "@/components/collections/CollectionRulesTab.svelte";
    import CollectionUpdateConfirm from "@/components/collections/CollectionUpdateConfirm.svelte";
    import SchemaChat from "@/components/collections/SchemaChat.svelte";

    const TAB_FIELDS = "fields";
    const TAB_AI = "ai_assistant";
    const TAB_RULES = "api_rules";
    const TAB_OPTIONS = "options";

    const TYPE_BASE = "base";
    const TYPE_AUTH = "auth";
    const TYPE_VIEW = "view";

    const collectionTypes = {};
    collectionTypes[TYPE_BASE] = "Base";
    collectionTypes[TYPE_VIEW] = "View";
    collectionTypes[TYPE_AUTH] = "Auth";

    // Auto-seed presets
    const seedPresets = [
        { value: 0, label: "None" },
        { value: 10, label: "10" },
        { value: 100, label: "100" },
        { value: 1000, label: "1K" },
        { value: 10000, label: "10K" },
        { value: 100000, label: "100K" },
    ];

    const dispatch = createEventDispatcher();

    let collectionPanel;
    let confirmChangesPanel;
    let original = null;
    let collection = {};
    let isSaving = false;
    let isLoadingConfirmation = false;
    let confirmClose = false; // prevent close recursion
    let activeTab = TAB_FIELDS;
    let initialFormHash = calculateFormHash(collection);
    let fieldsTabError = "";
    let baseCollectionKeys = [];

    // Auto-seed options for new collections
    let autoSeedCount = 0;
    let autoSeedDescription = "";
    let isSeedingAfterCreate = false;

    $: baseCollectionKeys = Object.keys($scaffolds["base"] || {});

    $: isAuth = collection.type === TYPE_AUTH;

    $: isView = collection.type === TYPE_VIEW;

    $: if ($errors.fields || $errors.viewQuery || $errors.indexes) {
        // extract the direct fields list error, otherwise - return a generic message
        fieldsTabError = CommonHelper.getNestedVal($errors, "fields.message") || "Has errors";
    } else {
        fieldsTabError = "";
    }

    $: isSystemUpdate = !!collection.id && collection.system;

    $: isSuperusers = !!collection.id && collection.system && collection.name == "_superusers";

    $: hasChanges = initialFormHash != calculateFormHash(collection);

    $: canSave = !collection.id || hasChanges;

    $: if (activeTab === TAB_OPTIONS && collection.type !== "auth") {
        // reset selected tab
        changeTab(TAB_FIELDS);
    }

    $: if (collection.type === "view") {
        // reset non-view fields
        collection.createRule = null;
        collection.updateRule = null;
        collection.deleteRule = null;
        collection.indexes = [];
    }

    // update indexes on collection rename
    $: if (collection.name && original?.name != collection.name && collection.indexes.length > 0) {
        collection.indexes = collection.indexes?.map((idx) =>
            CommonHelper.replaceIndexTableName(idx, collection.name),
        );
    }

    export function changeTab(newTab) {
        activeTab = newTab;
    }

    export function show(model) {
        load(model);

        confirmClose = true;
        isLoadingConfirmation = false;
        isSaving = false;

        changeTab(TAB_FIELDS);

        return collectionPanel?.show();
    }

    export function hide() {
        return collectionPanel?.hide();
    }

    export function forceHide() {
        confirmClose = false;
        hide();
    }

    async function load(model) {
        setErrors({}); // reset errors

        // Reset auto-seed options
        autoSeedCount = 0;
        autoSeedDescription = "";
        isSeedingAfterCreate = false;

        if (typeof model !== "undefined") {
            original = model;
            collection = structuredClone(model);
        } else {
            original = null;
            collection = structuredClone($scaffolds["base"]);

            // add default timestamp fields
            collection.fields.push({
                type: "autodate",
                name: "created",
                onCreate: true,
            });
            collection.fields.push({
                type: "autodate",
                name: "updated",
                onCreate: true,
                onUpdate: true,
            });
        }

        // normalize
        collection.fields = collection.fields || [];
        collection._originalName = collection.name || "";

        await tick();

        initialFormHash = calculateFormHash(collection);
    }

    async function saveConfirm(hideAfterSave = true) {
        if (isLoadingConfirmation) {
            return;
        }

        isLoadingConfirmation = true;

        try {
            if (!collection.id) {
                await save(hideAfterSave);
            } else {
                await confirmChangesPanel?.show(original, collection, hideAfterSave);
            }
        } catch {}

        isLoadingConfirmation = false;
    }

    async function save(hideAfterSave = true) {
        if (isSaving) {
            return;
        }

        isSaving = true;

        const data = exportFormData();
        const isNew = !collection.id;
        const shouldAutoSeed = isNew && autoSeedCount > 0 && !isView;

        try {
            let result;
            if (isNew) {
                result = await ApiClient.collections.create(data);
            } else {
                result = await ApiClient.collections.update(collection.id, data);
            }

            removeAllToasts();

            addCollection(result);

            addSuccessToast(
                !collection.id ? "Successfully created collection." : "Successfully updated collection.",
            );

            dispatch("save", {
                isNew: isNew,
                collection: result,
            });

            if (isNew) {
                $activeCollection = result;

                await refreshScaffolds();

                // Auto-seed if requested
                if (shouldAutoSeed) {
                    await performAutoSeed(result);
                }
            }

            if (hideAfterSave) {
                confirmClose = false;
                hide();
            } else {
                load(result);
            }
        } catch (err) {
            ApiClient.error(err);
        }

        isSaving = false;
    }

    async function performAutoSeed(createdCollection) {
        isSeedingAfterCreate = true;
        addInfoToast(`Seeding ${(autoSeedCount || 0).toLocaleString()} records...`);

        try {
            const seedResult = await ApiClient.ai.generateSeedData({
                collectionId: createdCollection.id,
                count: autoSeedCount,
                description: autoSeedDescription || undefined,
            });

            if (seedResult.created > 0) {
                addSuccessToast(
                    `Auto-seeded ${seedResult.created.toLocaleString()} record${seedResult.created !== 1 ? "s" : ""}` +
                    (seedResult.mode === "hybrid" ? " (fast mode)" : "")
                );
                dispatch("seeded", seedResult);
            }
        } catch (err) {
            ApiClient.error(err, false);
            addInfoToast("Collection created, but auto-seed failed: " + (err?.data?.message || err?.message));
        }

        isSeedingAfterCreate = false;
    }

    function exportFormData() {
        const data = Object.assign({}, collection);
        data.fields = data.fields.slice(0);

        // remove deleted fields
        for (let i = data.fields.length - 1; i >= 0; i--) {
            const field = data.fields[i];
            if (field._toDelete) {
                data.fields.splice(i, 1);
            }
        }

        return data;
    }

    function truncateConfirm() {
        if (!original?.id) {
            return; // nothing to truncate
        }

        confirm(
            `Do you really want to delete all "${original.name}" records, including their cascade delete references and files?`,
            () => {
                return ApiClient.collections
                    .truncate(original.id)
                    .then(() => {
                        forceHide();
                        addSuccessToast(`Successfully truncated collection "${original.name}".`);
                        dispatch("truncate");
                    })
                    .catch((err) => {
                        ApiClient.error(err);
                    });
            },
        );
    }

    function deleteConfirm() {
        if (!original?.id) {
            return; // nothing to delete
        }

        confirm(`Do you really want to delete collection "${original.name}" and all its records?`, () => {
            return ApiClient.collections
                .delete(original.id)
                .then(() => {
                    forceHide();
                    addSuccessToast(`Successfully deleted collection "${original.name}".`);
                    dispatch("delete", original);
                    removeCollection(original);
                })
                .catch((err) => {
                    ApiClient.error(err);
                });
        });
    }

    function calculateFormHash(m) {
        return JSON.stringify(m);
    }

    function setCollectionType(t) {
        collection.type = t;

        // merge with the scaffold to ensure that the minimal props are set
        collection = Object.assign(structuredClone($scaffolds[t]), collection);

        // reset fields list errors on type change
        removeError("fields");
    }

    function duplicateConfirm() {
        if (hasChanges) {
            confirm("You have unsaved changes. Do you really want to discard them?", () => {
                duplicate();
            });
        } else {
            duplicate();
        }
    }

    async function duplicate() {
        const clone = original ? structuredClone(original) : null;

        if (clone) {
            clone.id = "";
            clone.created = "";
            clone.updated = "";
            clone.name += "_duplicate";

            // reset the fields list
            if (!CommonHelper.isEmpty(clone.fields)) {
                for (const field of clone.fields) {
                    field.id = "";
                }
            }

            // update indexes with the new table name
            if (!CommonHelper.isEmpty(clone.indexes)) {
                for (let i = 0; i < clone.indexes.length; i++) {
                    const parsed = CommonHelper.parseIndex(clone.indexes[i]);
                    parsed.indexName = "idx_" + CommonHelper.randomString(10);
                    parsed.tableName = clone.name;
                    clone.indexes[i] = CommonHelper.buildIndex(parsed);
                }
            }
        }

        show(clone);

        await tick();

        initialFormHash = "";
    }

    function hasOtherKeys(obj, excludes = []) {
        if (CommonHelper.isEmpty(obj)) {
            return false;
        }

        const errorKeys = Object.keys(obj);
        for (let key of errorKeys) {
            if (!excludes.includes(key)) {
                return true;
            }
        }

        return false;
    }
</script>

<!-- svelte-ignore a11y-no-noninteractive-tabindex -->
<!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
<OverlayPanel
    bind:this={collectionPanel}
    class="overlay-panel-lg colored-header collection-panel compact-header"
    escClose={false}
    overlayClose={!isSaving}
    beforeHide={() => {
        if (hasChanges && confirmClose) {
            confirm("You have unsaved changes. Do you really want to close the panel?", () => {
                confirmClose = false;
                hide();
            });
            return false;
        }
        return true;
    }}
    on:hide
    on:show
>
    <svelte:fragment slot="header">
        <!-- Compact header row: Name input + Type selector + Options menu -->
        <div class="compact-header-row">
            <form
                class="name-form"
                on:submit|preventDefault={() => {
                    canSave && saveConfirm();
                }}
            >
                <!-- svelte-ignore a11y-autofocus -->
                <input
                    type="text"
                    class="name-input"
                    required
                    disabled={isSystemUpdate}
                    spellcheck="false"
                    class:txt-bold={collection.system}
                    autofocus={!collection.id}
                    placeholder={!collection.id ? "collection_name" : ""}
                    value={collection.name}
                    on:input={(e) => {
                        collection.name = CommonHelper.slugify(e.target.value);
                        e.target.value = collection.name;
                    }}
                />
                <input type="submit" class="hidden" tabindex="-1" />
            </form>

            <div
                tabindex={!collection.id ? 0 : -1}
                role={!collection.id ? "button" : ""}
                aria-label="View types"
                class="type-selector"
                class:clickable={!collection.id}
            >
                <span class="type-label">{collectionTypes[collection.type] || "N/A"}</span>
                {#if !collection.id}
                    <i class="ri-arrow-down-s-fill" aria-hidden="true" />
                    <Toggler class="dropdown dropdown-right dropdown-nowrap m-t-5">
                        {#each Object.entries(collectionTypes) as [type, label]}
                            <button
                                type="button"
                                role="menuitem"
                                class="dropdown-item closable"
                                class:selected={type == collection.type}
                                on:click={() => setCollectionType(type)}
                            >
                                <i
                                    class={CommonHelper.getCollectionTypeIcon(type)}
                                    aria-hidden="true"
                                />
                                <span class="txt">{label}</span>
                            </button>
                        {/each}
                    </Toggler>
                {/if}
            </div>

            {#if !!collection.id && (!collection.system || !isView)}
                <div
                    tabindex="0"
                    role="button"
                    aria-label="More collection options"
                    class="btn btn-sm btn-circle btn-transparent flex-gap-0"
                >
                    <i class="ri-more-line" aria-hidden="true" />
                    <Toggler class="dropdown dropdown-right m-t-5">
                        {#if !collection.system}
                            <button
                                type="button"
                                class="dropdown-item"
                                role="menuitem"
                                on:click={() => duplicateConfirm()}
                            >
                                <i class="ri-file-copy-line" aria-hidden="true" />
                                <span class="txt">Duplicate</span>
                            </button>
                            <hr />
                        {/if}
                        {#if !isView}
                            <button
                                type="button"
                                class="dropdown-item txt-danger"
                                role="menuitem"
                                on:click={() => truncateConfirm()}
                            >
                                <i class="ri-eraser-line" aria-hidden="true"></i>
                                <span class="txt">Truncate</span>
                            </button>
                        {/if}
                        {#if !collection.system}
                            <button
                                type="button"
                                class="dropdown-item txt-danger"
                                role="menuitem"
                                on:click|preventDefault|stopPropagation={() => deleteConfirm()}
                            >
                                <i class="ri-delete-bin-7-line" aria-hidden="true" />
                                <span class="txt">Delete</span>
                            </button>
                        {/if}
                    </Toggler>
                </div>
            {/if}
        </div>

        <!-- Compact tabs -->
        <div class="tabs-header compact-tabs">
            {#if isView}
                <button
                    type="button"
                    class="tab-item"
                    class:active={activeTab === TAB_FIELDS}
                    on:click={() => changeTab(TAB_FIELDS)}
                >
                    <span class="txt">Query</span>
                    {#if !CommonHelper.isEmpty(fieldsTabError)}
                        <i
                            class="ri-error-warning-fill txt-danger"
                            transition:scale={{ duration: 150, start: 0.7 }}
                            use:tooltip={fieldsTabError}
                        />
                    {/if}
                </button>
            {:else}
                <button
                    type="button"
                    class="tab-item"
                    class:active={activeTab === TAB_FIELDS}
                    on:click={() => changeTab(TAB_FIELDS)}
                >
                    <i class="ri-list-check" aria-hidden="true" />
                    <span class="txt">Fields</span>
                    {#if !CommonHelper.isEmpty(fieldsTabError)}
                        <i
                            class="ri-error-warning-fill txt-danger"
                            transition:scale={{ duration: 150, start: 0.7 }}
                            use:tooltip={fieldsTabError}
                        />
                    {/if}
                </button>
                <button
                    type="button"
                    class="tab-item ai-tab"
                    class:active={activeTab === TAB_AI}
                    on:click={() => changeTab(TAB_AI)}
                >
                    <i class="ri-robot-line" aria-hidden="true" />
                    <span class="txt">AI Assistant</span>
                </button>
            {/if}

            {#if !isSuperusers}
                <button
                    type="button"
                    class="tab-item"
                    class:active={activeTab === TAB_RULES}
                    on:click={() => changeTab(TAB_RULES)}
                >
                    <span class="txt">API Rules</span>
                    {#if !CommonHelper.isEmpty($errors?.listRule) || !CommonHelper.isEmpty($errors?.viewRule) || !CommonHelper.isEmpty($errors?.createRule) || !CommonHelper.isEmpty($errors?.updateRule) || !CommonHelper.isEmpty($errors?.deleteRule) || !CommonHelper.isEmpty($errors?.authRule) || !CommonHelper.isEmpty($errors?.manageRule)}
                        <i
                            class="ri-error-warning-fill txt-danger"
                            transition:scale={{ duration: 150, start: 0.7 }}
                            use:tooltip={"Has errors"}
                        />
                    {/if}
                </button>
            {/if}

            {#if isAuth}
                <button
                    type="button"
                    class="tab-item"
                    class:active={activeTab === TAB_OPTIONS}
                    on:click={() => changeTab(TAB_OPTIONS)}
                >
                    <span class="txt">Options</span>
                    {#if $errors && hasOtherKeys($errors, baseCollectionKeys.concat( ["manageRule", "authRule"], ))}
                        <i
                            class="ri-error-warning-fill txt-danger"
                            transition:scale={{ duration: 150, start: 0.7 }}
                            use:tooltip={"Has errors"}
                        />
                    {/if}
                </button>
            {/if}
        </div>
    </svelte:fragment>

    <div class="tabs-content full-height">
        <!-- Fields tab (or Query for views) -->
        {#if activeTab === TAB_FIELDS}
            <div class="tab-item active">
                {#if isView}
                    <CollectionQueryTab bind:collection />
                {:else}
                    <CollectionFieldsTab bind:collection />
                {/if}
            </div>
        {/if}

        <!-- AI Assistant tab (only for non-view collections) -->
        {#if !isView && activeTab === TAB_AI}
            <div class="tab-item active ai-tab-content">
                <SchemaChat bind:collection on:applied={() => changeTab(TAB_FIELDS)} />
            </div>
        {/if}

        <!-- API Rules tab -->
        {#if !isSuperusers && activeTab === TAB_RULES}
            <div class="tab-item active">
                <CollectionRulesTab bind:collection />
            </div>
        {/if}

        <!-- Options tab (auth collections only) -->
        {#if isAuth && activeTab === TAB_OPTIONS}
            <div class="tab-item active">
                <CollectionAuthOptionsTab bind:collection />
            </div>
        {/if}
    </div>

    <svelte:fragment slot="footer">
        <!-- Auto-seed option for new non-view collections -->
        {#if !collection.id && !isView}
            <div class="auto-seed-section" transition:slide={{ duration: 150 }}>
                <div class="auto-seed-toggle">
                    <i class="ri-seedling-line" aria-hidden="true" />
                    <span class="auto-seed-label">Auto-seed:</span>
                    <div class="seed-presets">
                        {#each seedPresets as preset}
                            <button
                                type="button"
                                class="preset-chip"
                                class:active={autoSeedCount === preset.value}
                                on:click={() => (autoSeedCount = preset.value)}
                                disabled={isSaving}
                            >
                                {preset.label}
                            </button>
                        {/each}
                    </div>
                    {#if autoSeedCount > 0}
                        <input
                            type="text"
                            class="seed-description-input"
                            placeholder="Data context (optional)..."
                            bind:value={autoSeedDescription}
                            disabled={isSaving}
                        />
                    {/if}
                </div>
            </div>
        {/if}

        <div class="footer-buttons">
            <button type="button" class="btn btn-transparent" disabled={isSaving} on:click={() => hide()}>
                <span class="txt">Cancel</span>
            </button>

            <div class="btns-group no-gap">
                <button
                    type="button"
                    title="Save and close"
                    class="btn"
                    class:btn-expanded={!collection.id}
                    class:btn-expanded-sm={!!collection.id}
                    class:btn-loading={isSaving || isLoadingConfirmation || isSeedingAfterCreate}
                    class:btn-success={!collection.id && autoSeedCount > 0}
                    disabled={!canSave || isSaving || isLoadingConfirmation}
                    on:click={() => saveConfirm()}
                >
                    {#if !collection.id && autoSeedCount > 0}
                        <i class="ri-seedling-line" aria-hidden="true" />
                        <span class="txt">Create + Seed {(autoSeedCount || 0).toLocaleString()}</span>
                    {:else}
                        <span class="txt">{!collection.id ? "Create" : "Save changes"}</span>
                    {/if}
                </button>

                {#if collection.id}
                    <button
                        type="button"
                        class="btn p-l-5 p-r-5 flex-gap-0"
                        disabled={!canSave || isSaving || isLoadingConfirmation}
                    >
                        <i class="ri-arrow-down-s-line" aria-hidden="true"></i>

                        <Toggler class="dropdown dropdown-upside dropdown-right dropdown-nowrap m-b-5">
                            <button
                                type="button"
                                class="dropdown-item closable"
                                role="menuitem"
                                on:click={() => saveConfirm(false)}
                            >
                                <span class="txt">Save and continue</span>
                            </button>
                        </Toggler>
                    </button>
                {/if}
            </div>
        </div>
    </svelte:fragment>
</OverlayPanel>

<CollectionUpdateConfirm bind:this={confirmChangesPanel} on:confirm={(e) => save(e.detail)} />

<style>
    /* Compact header styles */
    :global(.compact-header .panel-header) {
        padding: 12px 20px 0 20px !important;
        gap: 8px !important;
    }

    .compact-header-row {
        display: flex;
        align-items: center;
        gap: 10px;
        width: 100%;
    }

    .name-form {
        flex: 1;
        min-width: 0;
    }

    .name-input {
        width: 100%;
        padding: 8px 12px;
        border: 1px solid var(--borderColor);
        border-radius: var(--baseRadius);
        font-size: 1.1em;
        font-weight: 600;
        background: var(--baseAlt1Color);
        color: var(--txtPrimaryColor);
    }

    .name-input:focus {
        outline: none;
        border-color: var(--primaryColor);
        background: var(--baseColor);
    }

    .name-input::placeholder {
        font-weight: normal;
        opacity: 0.5;
    }

    .type-selector {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 8px 12px;
        background: var(--baseAlt2Color);
        border-radius: var(--baseRadius);
        font-size: 0.85em;
        font-weight: 500;
        color: var(--txtHintColor);
        white-space: nowrap;
    }

    .type-selector.clickable {
        cursor: pointer;
    }

    .type-selector.clickable:hover {
        background: var(--baseAlt1Color);
        color: var(--txtPrimaryColor);
    }

    .type-label {
        text-transform: uppercase;
        letter-spacing: 0.5px;
        font-size: 0.75em;
    }

    /* Compact tabs */
    .compact-tabs {
        margin-top: 8px !important;
        padding-bottom: 0 !important;
        border-bottom: none !important;
        gap: 4px;
    }

    .compact-tabs .tab-item {
        padding: 8px 14px !important;
        font-size: 0.85em !important;
        display: inline-flex;
        align-items: center;
        gap: 6px;
    }

    .compact-tabs .tab-item i {
        font-size: 1.1em;
    }

    .tabs-content:focus-within {
        z-index: 9; /* autocomplete dropdown overlay fix */
    }

    :global(.collection-panel .panel-content) {
        scrollbar-gutter: stable;
        padding-right: calc(var(--baseSpacing) - 5px);
    }

    /* Full height tabs content */
    :global(.collection-panel .panel-content) {
        display: flex;
        flex-direction: column;
    }

    .full-height {
        flex: 1;
        min-height: 0;
        display: flex;
        flex-direction: column;
    }

    .full-height > .tab-item {
        flex: 1;
        min-height: 0;
        display: flex;
        flex-direction: column;
        overflow: auto;
    }

    /* AI tab styling */
    .ai-tab {
        color: var(--successColor) !important;
    }

    .ai-tab.active {
        border-color: var(--successColor) !important;
    }

    .ai-tab i {
        color: var(--successColor);
    }

    .ai-tab-content {
        overflow: hidden !important;
    }

    .ai-tab-content:not(.active) {
        display: none !important;
    }

    /* Auto-seed section in footer */
    :global(.collection-panel .panel-footer) {
        flex-direction: column !important;
        gap: 12px !important;
    }

    .auto-seed-section {
        width: 100%;
        padding: 12px;
        background: var(--baseAlt1Color);
        border-radius: var(--baseRadius);
        margin-bottom: 4px;
    }

    .auto-seed-toggle {
        display: flex;
        align-items: center;
        gap: 10px;
        flex-wrap: wrap;
    }

    .auto-seed-toggle > i {
        color: var(--successColor);
        font-size: 1.1em;
    }

    .auto-seed-label {
        font-weight: 600;
        font-size: 0.9em;
        white-space: nowrap;
    }

    .seed-presets {
        display: flex;
        gap: 4px;
        flex-wrap: wrap;
    }

    .preset-chip {
        padding: 4px 10px;
        border: 1px solid var(--borderColor);
        border-radius: 14px;
        background: var(--baseColor);
        color: var(--txtPrimaryColor);
        font-size: 0.8em;
        font-weight: 500;
        cursor: pointer;
        transition: all 0.15s ease;
    }

    .preset-chip:hover:not(:disabled) {
        border-color: var(--successColor);
        background: rgba(16, 185, 129, 0.1);
    }

    .preset-chip.active {
        border-color: var(--successColor);
        background: var(--successColor);
        color: white;
    }

    .preset-chip:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .seed-description-input {
        flex: 1;
        min-width: 150px;
        padding: 6px 10px;
        border: 1px solid var(--borderColor);
        border-radius: var(--baseRadius);
        font-size: 0.85em;
        background: var(--baseColor);
    }

    .seed-description-input:focus {
        outline: none;
        border-color: var(--primaryColor);
    }

    .footer-buttons {
        display: flex;
        align-items: center;
        justify-content: space-between;
        width: 100%;
        gap: 10px;
    }

    /* Success button variant for create + seed */
    :global(.btn-success) {
        background: var(--successColor) !important;
        border-color: var(--successColor) !important;
    }

    :global(.btn-success:hover:not(:disabled)) {
        background: #059669 !important;
        border-color: #059669 !important;
    }
</style>
