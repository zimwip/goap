<script lang="ts">
  // The dependencies of a change (ADR 0108): the changes whose work must reach the branch it merges into before it
  // merges, the open changes waiting for it, and the order its open sub-changes merge in. The server refuses a cycle,
  // an ancestor, a sub-change or an abandoned change, and holds the merge until every dependency is satisfied.
  import { graph, errorMessage, type Change, type ChangeDependencies } from '../api';
  import { stamp, keyOf } from '../flux/signals.svelte';
  import { openTab } from '../shell/tabs.svelte';
  import { notify } from '../shell/workbench.svelte';
  import StatusBadge from './StatusBadge.svelte';
  import { dependenciesEditable, dependencyChoices, dependencyNote, setDependencies, withDependency, withoutDependency } from '../changeDependencies';

  let { change, onchange }: { change: Change; onchange?: () => void } = $props();

  let deps = $state<ChangeDependencies | undefined>();
  let choices = $state<Change[] | undefined>();
  let adding = $state(false);
  let target = $state('');
  let busy = $state(false);
  let error = $state('');

  const id = $derived(change.id ?? '');
  const editable = $derived(dependenciesEditable(change));
  const dependsOn = $derived(deps?.dependsOn ?? []);
  const dependents = $derived(deps?.dependents ?? []);
  const order = $derived(deps?.order ?? []);
  const open = (cid?: string) => cid && openTab({ kind: 'change', params: { id: cid } });

  $effect(() => {
    const c = id;
    if (!c) return;
    stamp(keyOf.change(c));
    for (const d of change.dependsOn ?? []) stamp(keyOf.change(d)); // a dependency merging satisfies it
    const ctrl = new AbortController();
    graph
      .getChangeDependencies(c, ctrl.signal)
      .then((d) => (deps = d))
      .catch((e) => {
        if (!ctrl.signal.aborted) error = errorMessage(e);
      });
    return () => ctrl.abort();
  });

  async function startAdding() {
    adding = true;
    error = '';
    try {
      const r = await graph.listChanges(undefined, { headerOnly: true });
      choices = dependencyChoices(change, r.changes ?? []);
      target = choices[0]?.id ?? '';
    } catch (e) {
      error = errorMessage(e);
    }
  }

  async function save(next: string[], done: string) {
    busy = true;
    error = '';
    try {
      await setDependencies(id, next);
      notify(done, 'ok');
      adding = false;
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if dependsOn.length || dependents.length || order.length || editable}
  <section class="deps" aria-label="Dependencies">
    <div class="head">
      <h3>Depends on</h3>
      {#if editable && !adding}<button type="button" class="small" onclick={startAdding}>Add dependency…</button>{/if}
    </div>
    {#if dependsOn.length}
      <ul>
        {#each dependsOn as d (d.id)}
          <li>
            <span class="mark" class:ok={d.satisfied} aria-hidden="true">{d.satisfied ? '✓' : '…'}</span>
            <button type="button" class="link" onclick={() => open(d.id)}>{d.title || d.id}</button>
            <StatusBadge status={d.status} />
            <span class="hint">{dependencyNote(d)}</span>
            {#if editable}
              <button type="button" class="small" disabled={busy} title="Remove the dependency"
                onclick={() => save(withoutDependency(change.dependsOn, d.id ?? ''), 'Dependency removed.')}>Remove</button>
            {/if}
          </li>
        {/each}
      </ul>
    {:else}
      <p class="hint">No dependency: it merges whenever its guardians agree.</p>
    {/if}
    {#if adding}
      <form class="add" onsubmit={(e) => (e.preventDefault(), save(withDependency(change.dependsOn, target), 'Dependency added.'))}>
        {#if !choices}
          <span class="hint">Reading the changes…</span>
        {:else if !choices.length}
          <span class="hint">No open change to depend on.</span>
        {:else}
          <label for="dep-target">Depends on change</label>
          <select id="dep-target" bind:value={target}>
            {#each choices as c (c.id)}<option value={c.id}>{c.title || c.id} ({c.methodology || 'no methodology'}, {c.status})</option>{/each}
          </select>
          <p class="hint">This change will merge only once the work of the one chosen has reached the branch it merges into.</p>
        {/if}
        <div class="row">
          {#if choices?.length}<button type="submit" class="primary small" disabled={busy || !target}>Add</button>{/if}
          <button type="button" class="small" onclick={() => (adding = false)}>Cancel</button>
        </div>
      </form>
    {/if}
    {#if dependents.length}
      <h3>Needed by</h3>
      <ul>
        {#each dependents as d (d.id)}
          <li>
            <button type="button" class="link" onclick={() => open(d.id)}>{d.title || d.id}</button>
            <StatusBadge status={d.status} />
            {#if d.satisfied}<span class="hint">has what it needs from this change</span>{/if}
          </li>
        {/each}
      </ul>
    {/if}
    {#if order.length}
      <h3>Merge order of its sub-changes</h3>
      <ol>
        {#each order as d (d.id)}
          <li>
            <button type="button" class="link" onclick={() => open(d.id)}>{d.title || d.id}</button>
            <StatusBadge status={d.status} />
            <span class="hint">{d.satisfied ? 'ready as far as its dependencies go' : d.reason}</span>
          </li>
        {/each}
      </ol>
    {/if}
    {#if error}<div class="alert small">{error}</div>{/if}
  </section>
{/if}

<style>
  .deps {
    margin: 0.75rem 0;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  h3 {
    font-size: 0.9rem;
    margin: 0.5rem 0 0.25rem;
  }
  ul,
  ol {
    margin: 0;
    padding-left: 1.2rem;
  }
  ul {
    list-style: none;
    padding-left: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    margin: 2px 0;
  }
  ol li {
    display: list-item;
  }
  .mark {
    width: 1em;
    color: var(--muted);
  }
  .mark.ok {
    color: var(--ok);
  }
  .hint {
    font-size: 0.85em;
    color: var(--muted);
    margin: 0;
  }
  .add {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0.4rem 0;
  }
  label {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .row {
    display: flex;
    gap: 6px;
  }
  .alert.small {
    font-size: 0.88em;
  }
</style>
