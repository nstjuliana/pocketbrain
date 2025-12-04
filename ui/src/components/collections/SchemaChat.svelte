<script>
    import { slide } from "svelte/transition";
    import { createEventDispatcher } from "svelte";
    import ApiClient from "@/utils/ApiClient";
    import { addErrorToast, addSuccessToast } from "@/stores/toasts";

    // Field type display info
    const fieldTypeInfo = {
        text: { icon: "ri-text", label: "Text" },
        number: { icon: "ri-hashtag", label: "Number" },
        bool: { icon: "ri-toggle-line", label: "Bool" },
        email: { icon: "ri-mail-line", label: "Email" },
        url: { icon: "ri-link", label: "URL" },
        editor: { icon: "ri-edit-2-line", label: "Editor" },
        date: { icon: "ri-calendar-line", label: "Date" },
        select: { icon: "ri-list-check", label: "Select" },
        json: { icon: "ri-code-s-slash-line", label: "JSON" },
        file: { icon: "ri-attachment-line", label: "File" },
        relation: { icon: "ri-link-m", label: "Relation" },
        autodate: { icon: "ri-calendar-event-line", label: "Autodate" },
        geoPoint: { icon: "ri-map-pin-line", label: "GeoPoint" },
    };

    function getFieldTypeInfo(type) {
        return fieldTypeInfo[type] || { icon: "ri-question-line", label: type };
    }

    const dispatch = createEventDispatcher();

    export let collection = {};

    // Working schema state - accumulates all AI-generated fields
    let workingSchema = {
        name: collection.name || "",
        type: collection.type || "base",
        fields: [],
    };

    let prompt = "";
    let messages = [];
    let isGenerating = false;
    let chatContainer;

    // Sync collection name/type changes
    $: if (collection.name && !workingSchema.name) {
        workingSchema.name = collection.name;
    }
    $: if (collection.type) {
        workingSchema.type = collection.type;
    }

    // Get non-system fields from working schema for AI context
    function getWorkingFields() {
        return workingSchema.fields
            .filter(f => !f.system)
            .map(f => ({ name: f.name, type: f.type }));
    }

    // Normalize a field to ensure all required properties exist
    function normalizeField(field) {
        const normalized = { ...field };
        delete normalized.id; // Treat as new field

        switch (field.type) {
            case "select":
                if (!Array.isArray(normalized.values)) {
                    normalized.values = normalized.options?.values || [];
                }
                if (typeof normalized.maxSelect === "undefined") {
                    normalized.maxSelect = 1;
                }
                if (normalized.values.length > 0 && normalized.maxSelect > normalized.values.length) {
                    normalized.maxSelect = normalized.values.length;
                }
                break;
            case "file":
                if (!Array.isArray(normalized.mimeTypes)) normalized.mimeTypes = [];
                if (!Array.isArray(normalized.thumbs)) normalized.thumbs = [];
                if (typeof normalized.maxSelect === "undefined") normalized.maxSelect = 1;
                if (typeof normalized.maxSize === "undefined") normalized.maxSize = 5242880;
                break;
            case "relation":
                if (typeof normalized.maxSelect === "undefined") normalized.maxSelect = 1;
                if (typeof normalized.cascadeDelete === "undefined") normalized.cascadeDelete = false;
                break;
            case "json":
                if (typeof normalized.maxSize === "undefined") normalized.maxSize = 0;
                break;
        }

        return normalized;
    }

    // Merge new fields into working schema
    function mergeFields(newFields, newName) {
        if (newName && !workingSchema.name) {
            workingSchema.name = newName;
        }

        const existingNames = new Set(workingSchema.fields.map(f => f.name));
        
        for (const field of newFields) {
            const normalized = normalizeField(field);
            if (existingNames.has(normalized.name)) {
                // Update existing field
                const idx = workingSchema.fields.findIndex(f => f.name === normalized.name);
                if (idx !== -1) {
                    workingSchema.fields[idx] = normalized;
                }
            } else {
                // Add new field
                workingSchema.fields = [...workingSchema.fields, normalized];
            }
        }
        
        workingSchema = workingSchema; // trigger reactivity
    }

    // Remove a field from working schema
    function removeField(fieldName) {
        workingSchema.fields = workingSchema.fields.filter(f => f.name !== fieldName);
        addSuccessToast(`Removed "${fieldName}" field`);
    }

    // Clear the working schema
    function clearSchema() {
        workingSchema = {
            name: "",
            type: collection.type || "base",
            fields: [],
        };
        messages = [];
        addSuccessToast("Schema cleared");
    }

    // System field names that should NEVER be added
    const SYSTEM_FIELD_NAMES = new Set(["id", "created", "updated", "collectionId", "collectionName", "expand"]);

    // Handle commands locally (remove, rename) without calling AI
    function tryLocalCommand(message) {
        const lower = message.toLowerCase().trim();
        
        // Check if this looks like a remove/delete command
        const isRemoveIntent = /\b(remove|delete|drop|del|get rid of)\b/i.test(lower);
        
        if (isRemoveIntent) {
            // Try to find a field name in the message
            for (const field of workingSchema.fields) {
                const fieldNameLower = field.name.toLowerCase();
                // Check if the field name appears in the message
                if (lower.includes(fieldNameLower)) {
                    workingSchema.fields = workingSchema.fields.filter(f => f.name !== field.name);
                    workingSchema = workingSchema; // trigger reactivity
                    return { handled: true, message: `Removed "${field.name}" field.` };
                }
            }
            // If we detected remove intent but couldn't find a field, show available fields
            if (workingSchema.fields.length > 0) {
                const fieldNames = workingSchema.fields.map(f => f.name).join(", ");
                return { 
                    handled: true, 
                    message: `Couldn't find that field. Available fields: ${fieldNames}`, 
                    error: true 
                };
            }
            return { handled: true, message: "No fields to remove.", error: true };
        }

        // Valid PocketBase field types with common aliases
        const TYPE_MAP = {
            "text": "text", "string": "text",
            "number": "number", "int": "number", "integer": "number", "float": "number",
            "bool": "bool", "boolean": "bool",
            "email": "email",
            "url": "url", "link": "url",
            "editor": "editor", "richtext": "editor", "html": "editor",
            "date": "date", "datetime": "date",
            "select": "select", "dropdown": "select", "enum": "select",
            "json": "json", "object": "json",
            "file": "file", "upload": "file", "image": "file",
            "relation": "relation", "ref": "relation",
            "autodate": "autodate",
            "geopoint": "geoPoint", "geo": "geoPoint", "location": "geoPoint",
        };
        const TYPE_ALIASES = Object.keys(TYPE_MAP);

        // Check if this looks like a TYPE change - "change X to text type", "make X a number"
        const hasTypeWord = /\btype\b/i.test(lower);
        const endsWithType = TYPE_ALIASES.some(t => lower.endsWith(t) || lower.endsWith(`${t} type`));
        const isTypeChangeIntent = hasTypeWord || endsWithType;
        
        if (isTypeChangeIntent) {
            let targetField = null;
            let newType = null;
            
            // Find field name in the message
            for (const field of workingSchema.fields) {
                if (lower.includes(field.name.toLowerCase())) {
                    targetField = field;
                    break;
                }
            }
            
            // Find the type
            for (const [alias, pbType] of Object.entries(TYPE_MAP)) {
                if (lower.includes(alias)) {
                    newType = pbType;
                    break;
                }
            }
            
            if (targetField && newType) {
                const oldType = targetField.type;
                targetField.type = newType;
                // Set type-specific defaults
                if (newType === "select") {
                    targetField.values = targetField.values || [];
                    targetField.maxSelect = targetField.maxSelect || 1;
                } else if (newType === "file") {
                    targetField.mimeTypes = targetField.mimeTypes || [];
                    targetField.maxSize = targetField.maxSize || 5242880;
                    targetField.maxSelect = targetField.maxSelect || 1;
                } else if (newType === "relation") {
                    targetField.collectionId = targetField.collectionId || "";
                    targetField.maxSelect = targetField.maxSelect || 1;
                }
                workingSchema.fields = [...workingSchema.fields];
                return { handled: true, message: `Changed "${targetField.name}" type: ${oldType} → ${newType}` };
            } else if (!targetField && workingSchema.fields.length > 0) {
                const fieldNames = workingSchema.fields.map(f => f.name).join(", ");
                return { handled: true, message: `Couldn't find field. Available: ${fieldNames}`, error: true };
            } else if (!newType) {
                return { handled: true, message: `Unknown type. Valid: text, number, bool, email, url, editor, date, select, json, file, relation`, error: true };
            }
        }

        // Check if this looks like a RENAME command (must NOT end with a type name)
        const isRenameIntent = /\b(rename|change|edit)\b.*\b(to|into)\b/i.test(lower) && 
                               !TYPE_ALIASES.some(t => lower.endsWith(t) || lower.endsWith(`${t} type`));
        
        if (isRenameIntent) {
            // Try to extract old and new names
            // Pattern: anything with "to" or "into" separating two words
            const renameMatch = lower.match(/["']?(\w+)["']?\s+(?:field\s+)?(?:name\s+)?(?:to|into)\s+["']?(\w+)["']?/i);
            if (renameMatch) {
                const oldName = renameMatch[1];
                const newName = renameMatch[2];
                
                // If newName is a type, skip (let type handler deal with it or fall through)
                if (TYPE_MAP[newName]) {
                    return { handled: false };
                }
                
                // Skip common words that aren't field names
                const skipWords = new Set(["the", "a", "an", "field", "column", "name", "rename", "change", "edit"]);
                if (skipWords.has(oldName)) {
                    // Try to find a field name before "to"
                    for (const field of workingSchema.fields) {
                        if (lower.includes(field.name.toLowerCase()) && 
                            lower.indexOf(field.name.toLowerCase()) < lower.indexOf(" to ")) {
                            const oldFieldName = field.name;
                            field.name = newName;
                            workingSchema.fields = [...workingSchema.fields];
                            return { handled: true, message: `Renamed "${oldFieldName}" → "${newName}".` };
                        }
                    }
                } else {
                    const existingField = workingSchema.fields.find(f => 
                        f.name.toLowerCase() === oldName.toLowerCase()
                    );
                    if (existingField) {
                        const oldFieldName = existingField.name;
                        existingField.name = newName;
                        workingSchema.fields = [...workingSchema.fields];
                        return { handled: true, message: `Renamed "${oldFieldName}" → "${newName}".` };
                    }
                }
            }
            
            // If we detected rename intent but couldn't parse it
            if (workingSchema.fields.length > 0) {
                const fieldNames = workingSchema.fields.map(f => f.name).join(", ");
                return { 
                    handled: true, 
                    message: `Couldn't parse rename. Try: "rename fieldname to newname". Available fields: ${fieldNames}`, 
                    error: true 
                };
            }
        }

        return { handled: false };
    }

    async function generateSchema() {
        if (!prompt.trim() || isGenerating) return;

        const userMessage = prompt.trim();
        prompt = "";
        messages = [...messages, { role: "user", content: userMessage }];

        scrollToBottom();

        // Try to handle locally first (remove, rename)
        const localResult = tryLocalCommand(userMessage);
        if (localResult.handled) {
            messages = [...messages, {
                role: "assistant",
                content: localResult.message,
                error: localResult.error,
            }];
            scrollToBottom();
            return;
        }

        isGenerating = true;

        try {
            const existingFields = getWorkingFields();
            const currentName = workingSchema.name || collection.name || null;

            const result = await ApiClient.ai.generateSchema({
                prompt: userMessage,
                collectionType: workingSchema.type,
                currentCollection: currentName,
                existingFields: existingFields.length > 0 ? existingFields : null,
            });

            // Filter out system fields from AI response
            const allFields = result.fields || [];
            const validFields = allFields.filter(f => {
                if (!f.name || !f.type) return false;
                if (SYSTEM_FIELD_NAMES.has(f.name.toLowerCase())) return false;
                return true;
            });

            // Check what was filtered
            const filteredOut = allFields.filter(f => 
                f.name && SYSTEM_FIELD_NAMES.has(f.name.toLowerCase())
            );

            if (validFields.length > 0) {
                mergeFields(validFields, result.name);
            }

            // Build confirmation message
            let responseContent;
            if (validFields.length > 0) {
                const action = existingFields.length > 0 ? "Added" : "Generated";
                const fieldNames = validFields.map(f => f.name).join(", ");
                responseContent = `${action} ${validFields.length} field${validFields.length !== 1 ? 's' : ''}: ${fieldNames}`;
            } else if (filteredOut.length > 0) {
                responseContent = `AI returned "${filteredOut.map(f => f.name).join(", ")}" which are system fields. Try being more specific, e.g., "Add a json field called metadata"`;
            } else if (allFields.length === 0) {
                responseContent = "AI returned no fields. Try rephrasing your request.";
            } else {
                responseContent = "No valid fields to add.";
            }

            messages = [...messages, {
                role: "assistant",
                content: responseContent,
            }];

            scrollToBottom();
        } catch (err) {
            messages = [...messages, {
                role: "assistant",
                content: `Error: ${err?.data?.message || err?.message || "Failed to generate schema"}`,
                error: true,
            }];
            ApiClient.error(err, false);
        }

        isGenerating = false;
    }

    function scrollToBottom() {
        setTimeout(() => {
            if (chatContainer) {
                chatContainer.scrollTop = chatContainer.scrollHeight;
            }
        }, 100);
    }

    // Apply the entire working schema to the collection
    function applySchema() {
        if (workingSchema.fields.length === 0) {
            addErrorToast("No fields to apply");
            return;
        }

        // Set collection name if we have one
        if (workingSchema.name) {
            collection.name = workingSchema.name;
        }

        // Get existing system fields
        const systemFields = (collection.fields || []).filter(f => f.system);
        const existingNonSystemNames = new Set(
            (collection.fields || []).filter(f => !f.system).map(f => f.name)
        );

        // Add new fields (don't duplicate)
        const newFields = workingSchema.fields.filter(f => !existingNonSystemNames.has(f.name));
        
        collection.fields = [...systemFields, ...(collection.fields || []).filter(f => !f.system), ...newFields];
        collection.fields = collection.fields; // trigger reactivity

        dispatch("applied", { schema: workingSchema });
        addSuccessToast(`Applied ${workingSchema.fields.length} fields to collection`);
    }

    function handleKeyPress(e) {
        if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            generateSchema();
        }
    }
</script>

<div class="schema-chat-container">
    <!-- Chat Panel -->
    <div class="chat-panel">
        <div class="chat-messages" bind:this={chatContainer}>
            {#if messages.length === 0}
                <div class="chat-message chat-message-system compact-welcome">
                    <div class="chat-message-content">
                        <p><strong>AI Schema Designer</strong> — Describe your schema:</p>
                        <p class="examples">"blog with title, content" · "add tags field" · "status: draft/published"</p>
                    </div>
                </div>
            {/if}

            {#each messages as message}
                <div class="chat-message chat-message-{message.role}" class:error={message.error}>
                    <div class="chat-message-content">
                        <p>{message.content}</p>
                    </div>
                </div>
            {/each}

            {#if isGenerating}
                <div class="chat-message chat-message-assistant">
                    <div class="chat-message-content">
                        <div class="loader loader-sm" />
                        <span class="txt">Generating...</span>
                    </div>
                </div>
            {/if}
        </div>

        <div class="chat-input-wrapper">
            <div class="chat-input-container">
                <textarea
                    class="chat-input"
                    placeholder="Describe fields to add..."
                    bind:value={prompt}
                    on:keydown={handleKeyPress}
                    disabled={isGenerating}
                    rows="2"
                />
                <button
                    type="button"
                    class="btn btn-sm"
                    class:btn-loading={isGenerating}
                    disabled={!prompt.trim() || isGenerating}
                    on:click={() => generateSchema()}
                >
                    <i class="ri-send-plane-line" aria-hidden="true" />
                </button>
            </div>
        </div>
    </div>

    <!-- Schema Preview Panel -->
    <div class="schema-panel">
        <div class="schema-panel-header">
            <div class="schema-title">
                <i class="ri-database-2-line" aria-hidden="true" />
                <input 
                    type="text" 
                    class="schema-name-input"
                    placeholder="collection_name"
                    bind:value={workingSchema.name}
                />
                <span class="label label-sm">{workingSchema.type}</span>
            </div>
            {#if workingSchema.fields.length > 0}
                <button 
                    type="button" 
                    class="btn btn-xs btn-transparent btn-hint"
                    on:click={clearSchema}
                >
                    <i class="ri-delete-bin-line" aria-hidden="true" />
                    Clear
                </button>
            {/if}
        </div>

        <div class="schema-fields">
            {#if workingSchema.fields.length === 0}
                <div class="empty-state">
                    <i class="ri-add-circle-line" aria-hidden="true" />
                    <p>No fields yet</p>
                    <p class="txt-hint">Use the chat to add fields</p>
                </div>
            {:else}
                {#each workingSchema.fields as field, index (field.name)}
                    <div class="schema-field" transition:slide={{ duration: 150 }}>
                        <div class="field-info">
                            <i class={getFieldTypeInfo(field.type).icon} aria-hidden="true" />
                            <span class="field-name">{field.name}</span>
                            <span class="field-type">{getFieldTypeInfo(field.type).label}</span>
                            {#if field.required}
                                <span class="label label-warning label-xs">Required</span>
                            {/if}
                            {#if field.type === "select" && field.values?.length}
                                <span class="field-options txt-hint">
                                    [{field.values.slice(0, 3).join(", ")}{field.values.length > 3 ? "..." : ""}]
                                </span>
                            {/if}
                        </div>
                        <button
                            type="button"
                            class="btn btn-xs btn-transparent btn-hint field-remove"
                            on:click={() => removeField(field.name)}
                            title="Remove field"
                        >
                            <i class="ri-close-line" aria-hidden="true" />
                        </button>
                    </div>
                {/each}
            {/if}
        </div>

        {#if workingSchema.fields.length > 0}
            <div class="schema-actions">
                <button
                    type="button"
                    class="btn btn-expanded"
                    on:click={applySchema}
                >
                    <i class="ri-check-line" aria-hidden="true" />
                    <span class="txt">Apply {workingSchema.fields.length} Field{workingSchema.fields.length !== 1 ? 's' : ''}</span>
                </button>
            </div>
        {/if}
    </div>
</div>

<style>
    .schema-chat-container {
        display: grid;
        grid-template-columns: 1fr 280px;
        gap: 12px;
        height: 100%;
        min-height: 400px;
    }

    /* Chat Panel */
    .chat-panel {
        display: flex;
        flex-direction: column;
        border: 1px solid var(--borderColor);
        border-radius: var(--borderRadius);
        background: var(--bgColor);
        overflow: hidden;
    }

    .chat-messages {
        flex: 1;
        overflow-y: auto;
        padding: 12px;
        display: flex;
        flex-direction: column;
        gap: 10px;
    }

    .chat-message {
        display: flex;
    }

    .chat-message-user {
        justify-content: flex-end;
    }

    .chat-message-user .chat-message-content {
        background: var(--primaryColor);
        color: var(--primaryTextColor);
        border-radius: var(--borderRadius);
        padding: 8px 12px;
        max-width: 85%;
        font-size: 0.9em;
    }

    .chat-message-assistant .chat-message-content,
    .chat-message-system .chat-message-content {
        background: var(--baseAlt1Color);
        border-radius: var(--borderRadius);
        padding: 8px 12px;
        max-width: 85%;
        font-size: 0.9em;
    }

    .chat-message-assistant.error .chat-message-content {
        background: var(--dangerAltColor);
        color: var(--dangerColor);
    }

    /* Compact welcome message */
    .compact-welcome .chat-message-content {
        max-width: 100%;
        padding: 10px 14px;
    }

    .compact-welcome .examples {
        font-size: 0.85em;
        color: var(--txtHintColor);
        font-style: italic;
    }

    .chat-message-content p {
        margin: 0 0 0.4em 0;
    }
    .chat-message-content p:last-child {
        margin-bottom: 0;
    }

    .chat-message-content ul {
        margin: 0.5em 0 0 0;
        padding-left: 1.2em;
        font-size: 0.9em;
    }

    .chat-message-content li {
        margin: 0.25em 0;
        color: var(--txtHintColor);
    }

    .chat-input-wrapper {
        border-top: 1px solid var(--borderColor);
        padding: 10px;
    }

    .chat-input-container {
        display: flex;
        gap: 8px;
        align-items: flex-end;
    }

    .chat-input {
        flex: 1;
        min-height: 38px;
        max-height: 80px;
        padding: 8px 10px;
        border: 1px solid var(--borderColor);
        border-radius: var(--borderRadius);
        font-family: inherit;
        font-size: 0.9em;
        resize: none;
        background: var(--baseAlt1Color);
    }

    .chat-input:focus {
        outline: none;
        border-color: var(--primaryColor);
    }

    .chat-input:disabled {
        opacity: 0.6;
    }

    /* Schema Panel */
    .schema-panel {
        display: flex;
        flex-direction: column;
        border: 1px solid var(--borderColor);
        border-radius: var(--borderRadius);
        background: var(--baseAlt1Color);
        overflow: hidden;
    }

    .schema-panel-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 10px 12px;
        background: var(--baseAlt2Color);
        border-bottom: 1px solid var(--borderColor);
    }

    .schema-title {
        display: flex;
        align-items: center;
        gap: 8px;
        flex: 1;
        min-width: 0;
    }

    .schema-title i {
        color: var(--primaryColor);
        flex-shrink: 0;
    }

    .schema-name-input {
        flex: 1;
        min-width: 0;
        border: none;
        background: transparent;
        font-weight: 600;
        font-size: 0.95em;
        padding: 2px 4px;
        border-radius: 4px;
    }

    .schema-name-input:hover,
    .schema-name-input:focus {
        background: var(--baseAlt1Color);
        outline: none;
    }

    .schema-name-input::placeholder {
        color: var(--txtHintColor);
        font-weight: normal;
    }

    .schema-fields {
        flex: 1;
        overflow-y: auto;
        padding: 8px 0;
    }

    .empty-state {
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        padding: 20px 10px;
        color: var(--txtHintColor);
        font-size: 0.85em;
        text-align: center;
        padding: 20px;
    }

    .empty-state i {
        font-size: 2em;
        margin-bottom: 8px;
        opacity: 0.5;
    }

    .empty-state p {
        margin: 0;
    }

    .schema-field {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 12px;
        gap: 8px;
    }

    .schema-field:hover {
        background: var(--baseAlt2Color);
    }

    .field-info {
        display: flex;
        align-items: center;
        gap: 8px;
        flex: 1;
        min-width: 0;
        font-size: 0.9em;
    }

    .field-info i {
        color: var(--txtHintColor);
        flex-shrink: 0;
    }

    .field-name {
        font-weight: 500;
    }

    .field-type {
        color: var(--txtHintColor);
        font-size: 0.85em;
    }

    .field-options {
        font-size: 0.8em;
        max-width: 100px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    .field-remove {
        opacity: 0;
        transition: opacity 0.15s;
    }

    .schema-field:hover .field-remove {
        opacity: 1;
    }

    .schema-actions {
        padding: 12px;
        border-top: 1px solid var(--borderColor);
    }
</style>
