<script lang="ts">
  // An LLM alias picker: authors always pick or type an alias name, never a raw
  // provider/model — administrators alone decide what an alias resolves to
  // (Platform settings). Typing a name that doesn't exist yet is allowed: saving
  // asks an administrator to configure it.
  import { modelChoices, isAvailableAlias } from '../stores/modelChoices.svelte';

  let {
    id,
    value = $bindable(),
    bad = false,
    disabled = false,
  }: {
    id: string;
    value: string;
    bad?: boolean;
    disabled?: boolean;
  } = $props();

  const defaultAlias = $derived(modelChoices.aliases.find((a) => a.alias === 'default'));
  const unknown = $derived(!!value.trim() && modelChoices.loaded && !isAvailableAlias(value));
</script>

<input
  {id}
  list="{id}-aliases"
  type="text"
  class="mono"
  bind:value
  class:bad={bad || unknown}
  data-path={id}
  placeholder="default{defaultAlias ? ` — ${defaultAlias.provider}/${defaultAlias.model}` : ''}"
  {disabled}
/>
<datalist id="{id}-aliases">
  {#each modelChoices.aliases as a (a.alias)}
    <option value={a.alias}>{a.alias} — {a.provider}/{a.model}</option>
  {/each}
</datalist>
{#if unknown}
  <span class="hint">Alias "{value.trim()}" isn't configured yet. Saving will ask an administrator to set it up.</span>
{:else if modelChoices.error}
  <span class="hint">Alias list unavailable: {modelChoices.error}</span>
{:else}
  <span class="hint">Administrators decide what an alias resolves to, in Platform settings.</span>
{/if}
