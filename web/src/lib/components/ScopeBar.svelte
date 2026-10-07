<script lang="ts">
  // The scope bar of a change (ADR 0032 §6): pick the flow you look at — the main flow or one option — before its
  // content. Picking is local to this editor; "Work on it" moves the active pointer, the option the change and its
  // agents work on (every call that names no flow goes to it).
  import { graph, errorMessage, type Flow } from '../api';
  import { MAIN_SCOPE, scopeColor, scopeKind, scopeStatus } from '../changeScope';
  import StatusBadge from './StatusBadge.svelte';

  let {
    changeId,
    options,
    scope = $bindable(MAIN_SCOPE),
    candidates,
    mainImpacts = 0,
    closed = false,
    writable = true,
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
    /** whether the scope can be edited */
    writable?: boolean;
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

<div class="scope-bar" class:option={scopeKind(scope) === 'Option'} style="--scope: {scopeColor(options, scope)}">
  <div class="banner" title="The tabs marked with a dot show this flow">
    <span class="kind">{scopeKind(scope)}</span>
    <span class="name">{scope === MAIN_SCOPE || !current ? 'Main flow' : current.option?.name}</span>
    <span class="hint" aria-live="polite">{scopeStatus(scope, writable, closed)}{#if scope === MAIN_SCOPE && open.length} · {open.length} open option(s) hold their own impacts, pick one to see them{:else if current?.option?.hypothesis} · {current.option.hypothesis}{/if}</span>
  </div>

  <div class="chips" role="tablist" aria-label="Scope of the change">
    <span class="label" title="Pick the flow the marked tabs show">Switch to</span>
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
    border-top: 5px solid var(--scope);
    border-radius: var(--radius-sm);
    padding: 6px 10px;
    margin-bottom: 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    background: var(--surface);
  }
  .scope-bar.option {
    background: color-mix(in srgb, var(--scope) 8%, var(--surface));
    border-color: var(--scope);
  }
  /* the scope, said loudly: a band across the top of the bar in the colour of the flow */
  .banner {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin: -6px -10px 0;
    padding: 8px 12px;
    background: color-mix(in srgb, var(--scope) 22%, var(--surface));
    border-bottom: 1px solid var(--scope);
  }
  .kind {
    background: var(--scope);
    color: var(--surface);
    font-weight: 700;
    font-size: 0.8em;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    padding: 3px 10px;
    border-radius: 4px;
  }
  .name {
    font-size: 1.3em;
    font-weight: 700;
    color: var(--text);
  }
  .banner .hint {
    font-size: 0.9em;
    color: var(--text);
    opacity: 0.8;
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
    font-size: 0.85em;
    font-weight: 600;
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
