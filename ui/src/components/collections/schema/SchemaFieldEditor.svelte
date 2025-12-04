<script>
    import tooltip from "@/actions/tooltip";
    import Field from "@/components/base/Field.svelte";
    import SchemaField from "@/components/collections/schema/SchemaField.svelte";

    export let field;
    export let key = "";
</script>

<SchemaField bind:field {key} on:rename on:remove on:duplicate {...$$restProps}>
    <svelte:fragment slot="options">
        <Field class="form-field m-b-sm" name="fields.{key}.maxSize" let:uniqueId>
            <label for={uniqueId}>Max size <small>(bytes)</small></label>
            <input
                type="number"
                id={uniqueId}
                step="1"
                min="0"
                max={Number.MAX_SAFE_INTEGER}
                value={field.maxSize || ""}
                on:input={(e) => (field.maxSize = parseInt(e.target.value, 10))}
                placeholder="Default to max ~5MB"
            />
        </Field>

        <Field class="form-field form-field-toggle" name="fields.{key}.convertURLs" let:uniqueId>
            <input type="checkbox" id={uniqueId} bind:checked={field.convertURLs} />
            <label for={uniqueId}>
                <span class="txt">Strip urls domain</span>
                <i
                    class="ri-information-line link-hint"
                    use:tooltip={{
                        text: `This could help making the editor content more portable between environments since there will be no local base url to replace.`,
                    }}
                />
            </label>
        </Field>

        <Field class="form-field form-field-toggle" name="fields.{key}.embeddable" let:uniqueId>
            <input type="checkbox" id={uniqueId} bind:checked={field.embeddable} />
            <label for={uniqueId}>
                <span class="txt">Enable vector embeddings</span>
                <i
                    class="ri-information-line link-hint"
                    use:tooltip={{
                        text: `When enabled, you can generate vector embeddings for this field's content to enable similarity search. HTML will be stripped for embedding. Requires AI to be configured in Settings.`,
                    }}
                />
            </label>
        </Field>
    </svelte:fragment>
</SchemaField>
