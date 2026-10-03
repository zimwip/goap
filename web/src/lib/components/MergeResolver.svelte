<script lang="ts">
  import { stamp, keyOf } from '../flux/signals.svelte';
  // Merging a branch into another by hand (ADR 0032 §2): what the merge does node by node, and a resolution for each
  // node changed on both sides. A node changed on one side only joins the target as is (no version written); a
  // resolved conflict writes one merge version (two parents) with the properties chosen here, or keeps the target.
  import { graph, errorMessage, type JsonValue, type MergeCandidate, type MergePlan, type Resolution } from '../api';

  let {
    namespace,
    from,
    into,
    submitLabel = 'Merge',
    onmerge,
  }: {
    namespace: string;
    from: string;
    into: string;
    submitLabel?: string;
    /** runs the merge with the resolutions (by node id); true when it went through */
    onmerge: (resolutions: Record<string, Resolution>) => Promise<boolean>;
  } = $props();

  type Pick = 'theirs' | 'ours' | 'skip';
  let plan = $state<MergePlan | undefined>();
  let error = $state('');
  let busy = $state(false);
  /** per conflicting node: the side taken for each conflicting property, or skip (the target stays as is) */
  let picks = $state<Record<string, { mode: 'merge' | 'skip'; props: Record<string, Pick> }>>({});

  async function load() {
    try {
      plan = (await graph.planMerge(namespace, from, into)).plan;
      for (const c of conflicting) picks[c.node ?? ''] ??= { mode: c.deleted ? 'skip' : 'merge', props: Object.fromEntries((c.conflicts ?? []).map((k) => [k, 'theirs' as Pick])) };
      error = '';
    } catch (e) {
      error = errorMessage(e);
    }
  }
  $effect(() => {
    if (!(namespace && from && into)) return;
    // either branch moved (a baseline landed on it): the plan and its conflicts are not the same any more
    void stamp(keyOf.branch(from));
    void stamp(keyOf.branch(into));
    void load();
  });

  const conflicting = $derived((plan?.candidates ?? []).filter((c) => c.conflicts?.length));
  const joining = $derived((plan?.candidates ?? []).filter((c) => !c.conflicts?.length));
  const show = (v: JsonValue | undefined) => (v === undefined || v === null ? '—' : typeof v === 'string' ? v : JSON.stringify(v));

  function resolution(c: MergeCandidate): Resolution {
    const p = picks[c.node ?? ''];
    if (!p || p.mode === 'skip') return { skip: true };
    const props: Record<string, JsonValue> = { ...((c.merged ?? {}) as Record<string, JsonValue>) };
    for (const [k, side] of Object.entries(p.props)) {
      const v = side === 'ours' ? c.oursProps?.[k] : c.theirsProps?.[k];
      if (v === undefined) delete props[k];
      else props[k] = v as JsonValue;
    }
    return { props };
  }

  async function merge() {
    busy = true;
    error = '';
    try {
      const resolutions = Object.fromEntries(conflicting.map((c) => [c.node ?? '', resolution(c)]));
      if (await onmerge(resolutions)) await load();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<div class="resolver">
  {#if plan}
    <p class="hint">
      Merging <code>{plan.from}</code> into <code>{plan.into}</code>: {joining.length} node(s) join as they are,
      {conflicting.length} changed on both sides need a resolution.
    </p>
    {#if joining.length}
      <details>
        <summary class="hint">Joining as they are ({joining.length})</summary>
        <ul class="join">
          {#each joining as c (c.node)}
            <li><code>{c.key}</code> <span class="hint">{c.type} · {c.kind}{c.deleted ? ' · deleted' : ''} · v{c.theirs?.version}</span></li>
          {/each}
        </ul>
      </details>
    {/if}
    {#each conflicting as c (c.node)}
      {@const p = picks[c.node ?? '']}
      {#if p}
        <div class="conflict">
          <div class="head">
            <strong class="mono">{c.key}</strong>
            <span class="hint">{c.type} · base v{c.ancestor?.version ?? '—'} · {plan.into} v{c.ours?.version ?? '—'} · {plan.from} v{c.theirs?.version}{c.deleted ? ' (deleted there)' : ''}</span>
            <span class="grow"></span>
            <label><input type="radio" bind:group={p.mode} value="merge" /> {c.deleted ? 'delete' : 'merge'}</label>
            <label><input type="radio" bind:group={p.mode} value="skip" /> keep {plan.into}</label>
          </div>
          {#if p.mode === 'merge' && !c.deleted}
            <table>
              <thead><tr><th>Property</th><th>base</th><th>{plan.into}</th><th>{plan.from}</th></tr></thead>
              <tbody>
                {#each c.conflicts ?? [] as k (k)}
                  <tr>
                    <td class="mono">{k}</td>
                    <td class="muted">{show(c.base?.[k] as JsonValue)}</td>
                    <td><label><input type="radio" bind:group={p.props[k]} value="ours" /> {show(c.oursProps?.[k] as JsonValue)}</label></td>
                    <td><label><input type="radio" bind:group={p.props[k]} value="theirs" /> {show(c.theirsProps?.[k] as JsonValue)}</label></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      {/if}
    {/each}
    <div class="row">
      <button type="button" class="primary" disabled={busy} onclick={merge}>{busy ? 'Merging…' : submitLabel}</button>
    </div>
  {/if}
  {#if error}<pre class="error">{error}</pre>{/if}
</div>

<style>
  .resolver {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .conflict {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 6px 8px;
  }
  .head,
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .head label {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  .grow {
    flex: 1;
  }
  table {
    width: 100%;
    margin-top: 4px;
  }
  td label {
    display: inline-flex;
    gap: 4px;
    align-items: baseline;
  }
  .join {
    margin: 4px 0;
    padding-left: 1rem;
  }
</style>
