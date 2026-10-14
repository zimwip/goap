<script lang="ts">
  // The `on:` of an activity or of the methodology (ADR 0036 §3, 0106, 0110): the events that run it again on a change.
  // The event is picked among the standard events and those the methodology declares; one it does not know is flagged
  // at once (the compiler refuses it), and the registry's issues on the row are shown under it.
  import IssueHints from '../../components/IssueHints.svelte';
  import type { Draft } from '../../stores/drafts.svelte';
  import { TRIGGER_EVENTS } from '../../api';
  import { EVENT_LABELS, SUBSCRIPTION_ORIGINS, declaredNames, unknownEvent, type SubscriptionForm } from '../../subscriptions';

  let {
    subs = $bindable(),
    draft,
    path,
    label = 'Reacts to',
    hint = '',
    newEvent = 'change.edited',
    newFrom = '',
  }: {
    subs: SubscriptionForm[];
    draft: Draft;
    /** the path of the activity ("processes[0].steps[1]"), "" for the methodology */
    path: string;
    label?: string;
    hint?: string;
    newEvent?: string;
    newFrom?: string;
  } = $props();

  const prefix = $derived(path ? `${path}.` : '');
  const declared = $derived(draft.form.events.filter((e) => e.name.trim()));
  const names = $derived(declaredNames(draft.form.events));
</script>

<div class="label">{label} {#if hint}<span class="opt">({hint})</span>{/if}</div>
{#each subs as sub, i (i)}
  {@const why = unknownEvent(sub.event, names)}
  <div class="on-row" class:bad={!!why || draft.bad(`${prefix}on[${i}]`)} data-path="{prefix}on[{i}]">
    <select bind:value={sub.event} aria-label="Event" class:bad={!!why} data-path="{prefix}on[{i}].event">
      {#if sub.event && why}<option value={sub.event}>{sub.event} (unknown)</option>{/if}
      <optgroup label="Standard">
        {#each TRIGGER_EVENTS as ev (ev)}<option value={ev}>{EVENT_LABELS[ev] ?? ev}</option>{/each}
      </optgroup>
      {#if declared.length}
        <optgroup label="This methodology">
          {#each declared as e (e.name)}<option value={e.name.trim()}>{e.name.trim()}{e.description.trim() ? ` — ${e.description.trim()}` : ''}</option>{/each}
        </optgroup>
      {/if}
    </select>
    <select bind:value={sub.from} aria-label="From" data-path="{prefix}on[{i}].from">
      {#each SUBSCRIPTION_ORIGINS as o (o)}<option value={o}>{o ? `from ${o}` : 'from self'}</option>{/each}
    </select>
    <input type="text" class="mono" aria-label="Filter" placeholder={'CEL over event, e.g. "intent" in event.fields'} bind:value={sub.filter} data-path="{prefix}on[{i}].filter" />
    <label class="supersede" title="An event arriving while the run is at work interrupts it and runs it again on the change as it is now"><input type="checkbox" bind:checked={sub.supersede} disabled={draft.readonly} data-path="{prefix}on[{i}].supersede" /> restarts</label>
    {#if !draft.readonly}<button type="button" class="small icon" aria-label="Remove the event" onclick={() => subs.splice(i, 1)}>✕</button>{/if}
  </div>
  {#if why && !draft.readonly}<p class="unknown">{why}</p>{/if}
  <IssueHints {draft} path="{prefix}on[{i}]" />
{/each}
{#if !draft.readonly}<button type="button" class="small" onclick={() => subs.push({ event: newEvent, filter: '', from: newFrom, supersede: false })}>+ Event</button>{/if}

<style>
  .on-row {
    display: grid;
    gap: 6px;
    margin-bottom: 4px;
    align-items: center;
    grid-template-columns: minmax(0, 1.3fr) minmax(0, 1fr) minmax(0, 2.5fr) auto auto;
  }
  .supersede {
    font-size: 0.8rem;
    white-space: nowrap;
  }
  .unknown {
    color: var(--danger);
    font-size: 0.8rem;
    margin: 0 0 4px;
  }
</style>
