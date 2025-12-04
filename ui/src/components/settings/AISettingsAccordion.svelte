<script>
    import { slide } from "svelte/transition";
    import ApiClient from "@/utils/ApiClient";
    import tooltip from "@/actions/tooltip";
    import { removeError, setErrors } from "@/stores/errors";
    import { addSuccessToast, addErrorToast } from "@/stores/toasts";
    import Field from "@/components/base/Field.svelte";
    import RedactedPasswordInput from "@/components/base/RedactedPasswordInput.svelte";
    import Accordion from "@/components/base/Accordion.svelte";

    export let formSettings = {};
    export let originalFormSettings = {};

    let maskApiKey = false;
    let isTesting = false;
    let testError = null;

    // Ensure ai object exists with all required properties (only once)
    if (!formSettings.ai) {
        formSettings.ai = { enabled: false, provider: "openai", model: "gpt-4o-mini", apiKey: "" };
    }
    // Ensure apiKey is always a string (not undefined) for binding to work
    if (formSettings.ai && (formSettings.ai.apiKey === undefined || formSettings.ai.apiKey === null)) {
        formSettings.ai.apiKey = "";
    }
    
    // Determine if there's a saved API key by checking if AI was enabled in the original settings
    // The API key is masked in responses, so we use "enabled" as a proxy indicator
    // If AI was enabled before, there must be a saved API key
    $: hasSavedApiKey = originalFormSettings?.ai?.enabled === true;
    
    // Initialize mask state - mask if there's a saved API key OR if the user entered a new one
    maskApiKey = hasSavedApiKey || !!(formSettings?.ai?.apiKey && formSettings.ai.apiKey.length > 0);

    $: if (!formSettings?.ai?.enabled) {
        removeError("ai");
    }

    $: isEnabled = !!formSettings?.ai?.enabled;

    async function testConnection() {
        if (!formSettings?.ai?.enabled) {
            return;
        }

        // Need an API key to test
        const apiKeyToTest = formSettings?.ai?.apiKey;
        if (!apiKeyToTest) {
            addErrorToast("Enter an API key to test the connection.");
            return;
        }

        isTesting = true;
        testError = null;

        try {
            // Test with the form's current credentials
            await ApiClient.ai.testConnection({
                provider: formSettings.ai.provider || "openai",
                model: formSettings.ai.model || "gpt-4o-mini",
                apiKey: apiKeyToTest,
            });
            addSuccessToast("AI connection test successful.");
            testError = null;
        } catch (err) {
            testError = err;
            addErrorToast("AI connection test failed.");
        }

        isTesting = false;
    }

    const models = [
        { value: "gpt-4o", label: "GPT-4o" },
        { value: "gpt-4o-mini", label: "GPT-4o Mini" },
        { value: "gpt-4-turbo", label: "GPT-4 Turbo" },
        { value: "gpt-3.5-turbo", label: "GPT-3.5 Turbo" },
    ];
</script>

<Accordion single>
    <svelte:fragment slot="header">
        <div class="inline-flex">
            <i class="ri-robot-line"></i>
            <span class="txt">AI Schema Designer</span>
        </div>

        <div class="flex-fill" />

        {#if formSettings?.ai?.enabled}
            <span class="label label-success">Enabled</span>
        {:else}
            <span class="label">Disabled</span>
        {/if}
    </svelte:fragment>
    <Field class="form-field form-field-toggle m-b-sm" name="ai.enabled" let:uniqueId>
        <input type="checkbox" id={uniqueId} bind:checked={formSettings.ai.enabled} />
        <label for={uniqueId}>
            <span class="txt">Enable AI Schema Designer</span>
            <i
                class="ri-information-line link-hint"
                use:tooltip={{
                    text: "Enable AI-powered schema generation using OpenAI. This allows you to create collection schemas using natural language.",
                    position: "right",
                }}
            />
        </label>
    </Field>

    {#if formSettings?.ai?.enabled}
        <div class="grid" transition:slide={{ duration: 150 }}>
            <div class="col-lg-6">
                <Field class="form-field required" name="ai.provider" let:uniqueId>
                    <label for={uniqueId}>Provider</label>
                    <select id={uniqueId} required bind:value={formSettings.ai.provider}>
                        <option value="openai">OpenAI</option>
                    </select>
                </Field>
            </div>

            <div class="col-lg-6">
                <Field class="form-field required" name="ai.model" let:uniqueId>
                    <label for={uniqueId}>Model</label>
                    <select id={uniqueId} required bind:value={formSettings.ai.model}>
                        {#each models as model}
                            <option value={model.value}>{model.label}</option>
                        {/each}
                    </select>
                </Field>
            </div>

            <div class="col-lg-12">
                <Field class="form-field required" name="ai.apiKey" let:uniqueId>
                    <label for={uniqueId}>API Key</label>
                    <RedactedPasswordInput
                        required
                        id={uniqueId}
                        bind:mask={maskApiKey}
                        bind:value={formSettings.ai.apiKey}
                        placeholder="sk-..."
                    />
                    <div class="help-block">
                        Your OpenAI API key. Keep it secure and never share it publicly.
                    </div>
                </Field>
            </div>

            <div class="col-lg-12">
                <button
                    type="button"
                    class="btn btn-outline"
                    class:btn-loading={isTesting}
                    disabled={isTesting || !formSettings?.ai?.apiKey}
                    on:click={() => testConnection()}
                >
                    <span class="txt">Test Connection</span>
                </button>
                {#if hasSavedApiKey && !formSettings?.ai?.apiKey}
                    <span class="txt-hint m-l-10">
                        <i class="ri-check-line txt-success"></i>
                        API key saved
                    </span>
                {:else if !formSettings?.ai?.apiKey}
                    <span class="txt-hint m-l-10">Enter API key to test</span>
                {/if}

                {#if testError}
                    <div class="alert alert-danger m-t-10">
                        {testError?.data?.message || testError?.message || "Connection test failed"}
                    </div>
                {/if}
            </div>
        </div>
    {/if}
</Accordion>

