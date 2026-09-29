<script lang="ts">
  // The scope bar of a change (ADR 0032 §6): pick the flow you look at — the main flow or one option — before its
  // content. Picking is local to this editor; "Work on it" moves the active pointer, the option the change and its
  // agents work on (every call that names no flow goes to it).
  import { graph, errorMessage, type Flow } from '../api';
  import { MAIN_SCOPE, scopeColor } from '../changeScope';
  import StatusBadge from './StatusBadge.svelte';

  let {
    changeId,
    options,
    scope = $bindable(MAIN_SCOPE),
    candidates,
    mainImpacts = 0,
    closed = false,
    onchange,
    oncompare,
  }: {
    changeId: string;
    options: Flow[];
    scope?: string;
    /** candidate change impacts of each option */
    candidates: Map<string, number>;
    mainImpacts?: number;
    closed?: boolean;
    onchange?: () => void;
    oncompare?: () => void;
  } = $props();

  let adding = $state(false);
  let name = $state('');
  let hypothesis = $state('');
  let workOnIt = $state(false);
  let showDecided = $state(false);
  let busy = $state(false);
  let error = $state('');

  const open = $derived(options.filter((o) => o.status === 'open'));
  const decided = $derived(options.filter((o) => o.status !== 'open'));
  const active = $derived(options.find((o) => o.active));
  const current = $derived(options.find((o) => o.id === scope));

  async function act(fn: () => Promise<unknown>) {
    busy = true;
    error = '';
    try {
      await fn();
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  const openOption = () =>
    act(async () => {
      const o = (await graph.openOption(changeId, name.trim(), hypothesis.trim(), workOnIt)).option;
      name = '';
      hypothesis = '';
      adding = false;
      if (o?.id) scope = o.id; // look at what was just opened
    });
</script>

<div class="scope-bar" style="--scope: {scopeColor(options, scope)}">
  <div class="chips" role="tablist" aria-label="Scope of the change">
    <span class="label">Scope</span>
    <button type="button" role="tab" class="chip" class:on={scope === MAIN_SCOPE} aria-selected={scope === MAIN_SCOPE} style="--c: {scopeColor(options, MAIN_SCOPE)}" onclick={() => (scope = MAIN_SCOPE)}>
      <span class="dot"></span> Main flow
      <span class="count" title="change impacts of the main flow">{mainImpacts}</span>
    </button>
    {#each open as o (o.id)}
      <button type="button" role="tab" class="chip" class:on={scope === o.id} aria-selected={scope === o.id} style="--c: {scopeColor(options, o.id ?? '')}" onclick={() => (scope = o.id ?? MAIN_SCOPE)} title={o.option?.hypothesis}>
        <span class="dot"></span> {o.option?.name}
        <StatusBadge status={o.optionStatus} />
        {#if o.active}<span class="active" title="The change and its agents work on this option">● active</span>{/if}
        <span class="count" title="change impacts declared on this option">{candidates.get(o.id ?? '') ?? 0}</span>
      </button>
    {/each}
    {#if decided.length}
      <button type="button" class="link small" onclick={() => (showDecided = !showDecided)}>{showDecided ? 'hide' : 'show'} {decided.length} decided</button>
      {#if showDecided}
        {#each decided as o (o.id)}
          <button type="button" role="tab" class="chip decided" class:on={scope === o.id} aria-selected={scope === o.id} style="--c: {scopeColor(options, o.id ?? '')}" onclick={() => (scope = o.id ?? MAIN_SCOPE)}>
            <span class="dot"></span> {o.option?.name} <StatusBadge status={o.optionStatus} />
          </button>
        {/each}
      {/if}
    {/if}
    <span class="grow"></span>
    {#if !closed}
      <button type="button" class="small" onclick={() => (adding = !adding)}>+ Option</button>
    {/if}
    {#if options.length}
      <button type="button" class="small" onclick={() => oncompare?.()}>Compare &amp; decide</button>
    {/if}
  </div>

  <div class="status">
    {#if scope === MAIN_SCOPE}
      <span>Looking at the <strong>main flow</strong>{#if open.length}: {open.length} open option(s) hold their own impacts, pick one to see them{/if}.</span>
    {:else if current}
      <span>Looking at the option <strong>{current.option?.name}</strong>{#if current.option?.hypothesis} — {current.option.hypothesis}{/if}. It sees the main flow plus what it changes.</span>
    {/if}
    <span class="grow"></span>
    {#if !closed}
      {#if active}
        <span class="hint">Agents work on <strong>{active.option?.name}</strong>.</span>
        {#if scope !== active.id}
          <button type="button" class="small" disabled={busy} title="Agents and edits that name no flow go to the main flow again" onclick={() => act(() => graph.activateOption(changeId, MAIN_SCOPE))}>Back to the main flow</button>
        {/if}
      {:else}
        <span class="hint">Agents work on the main flow.</span>
      {/if}
      {#if current?.status === 'open' && !current.active}
        <button type="button" class="small primary" disabled={busy} title="Agents and edits that name no flow go to this option" onclick={() => act(() => graph.activateOption(changeId, current?.id ?? ''))}>Work on it</button>
      {:else if current?.active}
        <button type="button" class="small" disabled={busy} onclick={() => act(() => graph.activateOption(changeId, MAIN_SCOPE))}>Stop working on it</button>
      {/if}
    {/if}
  </div>

  {#if adding}
    <form class="new" onsubmit={(e) => (e.preventDefault(), openOption())}>
      <input type="text" class="name" placeholder="Name" bind:value={name} />
      <input type="text" class="grow" placeholder="Hypothesis" bind:value={hypothesis} />
      <label class="check"><input type="checkbox" bind:checked={workOnIt} /> agents work on it</label>
      <button type="submit" disabled={busy || !name.trim()}>Open option</button>
      <button type="button" onclick={() => (adding = false)}>Cancel</button>
    </form>
  {/if}
  {#if error}<div class="alert">{error}</div>{/if}
</div>

<style>
  .scope-bar {
    border: 1px solid var(--border);
    border-top: 3px solid var(--scope);
    border-radius: var(--radius-sm);
    padding: 6px 10px;
    margin-bottom: 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    background: var(--surface);
  }
  .chips,
  .status,
  .new {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }
  .label {
    font-size: 0.8em;
    text-transform: uppercase;
    color: var(--muted);
    letter-spacing: 0.04em;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 2px 10px;
    background: transparent;
    cursor: pointer;
  }
  .chip.on {
    border-color: var(--c);
    box-shadow: inset 0 0 0 1px var(--c);
    font-weight: 600;
  }
  .chip.decided {
    opacity: 0.75;
  }
  .dot {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--c);
  }
  .active {
    color: var(--c);
    font-size: 0.85em;
  }
  .count {
    font-size: 0.8em;
    color: var(--muted);
  }
  .status {
    font-size: 0.9em;
  }
  .grow {
    flex: 1;
  }
  .new .name {
    width: 12em;
  }
  .new .grow {
    min-width: 12em;
  }
  .check {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  .small {
    font-size: 0.85em;
  }
</style>
