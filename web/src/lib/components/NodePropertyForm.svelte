<script lang="ts">
  import { untrack } from 'svelte';
  // Edition of the properties of a node: the ones its type declares, the ones it
  // has, and new ones. Only the changed values are returned.
  let {
    props,
    declared,
    typeName,
    busy = false,
    onsave,
    oncancel,
  }: {
    props: Record<string, unknown>;
    declared: string[];
    typeName: string;
    busy?: boolean;
    onsave: (patch: Record<string, unknown>) => Promise<boolean> | boolean;
    oncancel: () => void;
  } = $props();

  const text = (v: unknown): string => (v === undefined || v === null ? '' : typeof v === 'string' ? v : JSON.stringify(v));
  // the form is created each time it is opened: it starts from the values of that moment
  const start = untrack(() => Object.fromEntries([...new Set([...declared, ...Object.keys(props)])].map((k) => [k, text(props[k])])));
  let draft = $state<Record<string, string>>(start);
  let newKey = $state('');
  let newValue = $state('');
  let error = $state('');

  /** A value typed in the form: text, unless the property already holds a non-text value. */
  function parse(key: string, value: string): unknown {
    const cur = props[key];
    if (cur !== undefined && cur !== null && typeof cur !== 'string') {
      try {
        return JSON.parse(value);
      } catch {
        throw new Error(`“${key}” holds ${typeof cur === 'object' ? 'structured' : typeof cur} data: ${value} is not valid JSON`);
      }
    }
    return value;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = '';
    const patch: Record<string, unknown> = {};
    try {
      for (const [k, v] of Object.entries(draft)) if (v !== text(props[k])) patch[k] = parse(k, v);
      const nk = newKey.trim();
      if (nk) {
        if (nk in draft) throw new Error(`property “${nk}” is already in the form`);
        patch[nk] = newValue;
      }
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      return;
    }
    if (!Object.keys(patch).length) return oncancel();
    if (await onsave(patch)) oncancel();
  }
</script>

<form class="form" onsubmit={submit}>
  {#each Object.keys(draft) as k (k)}
    <div class="field">
      <label for="np-{k}">{k}{#if !declared.includes(k)} <span class="hint">(not declared by {typeName})</span>{/if}</label>
      {#if draft[k].length > 80 || draft[k].includes('\n')}
        <textarea id="np-{k}" rows="4" bind:value={draft[k]}></textarea>
      {:else}
        <input id="np-{k}" type="text" bind:value={draft[k]} />
      {/if}
    </div>
  {/each}
  <div class="newprop">
    <input type="text" class="mono" placeholder="new property" aria-label="New property name" bind:value={newKey} />
    <input type="text" placeholder="value" aria-label="New property value" bind:value={newValue} />
  </div>
  <p class="hint">Values are text; a property that already holds a number, boolean or list is edited as JSON. Only the changed properties are proposed.</p>
  {#if error}<div class="alert">{error}</div>{/if}
  <div class="row">
    <button type="submit" class="primary" disabled={busy}>{busy ? 'Proposing…' : 'Propose the changes'}</button>
    <button type="button" onclick={oncancel}>Cancel</button>
  </div>
</form>

<style>
  .form {
    display: grid;
    gap: 0.5rem;
    max-width: 52rem;
  }
  .newprop {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) 3fr;
    gap: 0.4rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
  }
</style>
