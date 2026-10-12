<script lang="ts">
  // Attaches a change to a change whose methodology integrates its own (ADR 0106): a release taking a candidate. The
  // changes offered are the open ones of another methodology in its namespace; the server stays the authority (the
  // methodology of the parent integrates the change's, the project and unit, no cycle). What the change committed is
  // rebased onto the branch of its new parent.
  import { errorMessage, graph, type Change } from '../api';
  import { attachTo, parentChoices } from '../changeAttach';

  let { change, onattached, oncancel }: { change: Change; onattached: () => void; oncancel: () => void } = $props();

  let choices = $state<Change[] | undefined>();
  let target = $state('');
  let busy = $state(false);
  let error = $state('');

  $effect(() => {
    const ch = change;
    void (async () => {
      try {
        const r = await graph.listChanges();
        choices = parentChoices(ch, r.changes ?? []);
        if (!choices.some((c) => c.id === target)) target = choices[0]?.id ?? '';
      } catch (e) {
        error = errorMessage(e);
      }
    })();
  });

  async function attach(e: SubmitEvent) {
    e.preventDefault();
    if (!change.id || !target) return;
    busy = true;
    error = '';
    try {
      await attachTo(change.id, target);
      onattached();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<form class="attach" onsubmit={attach}>
  {#if !choices}
    <span class="muted">Reading the changes…</span>
  {:else if !choices.length}
    <span class="muted">No open change of another methodology to attach it to.</span>
    <button type="button" class="small" onclick={oncancel}>Close</button>
  {:else}
    <label for="at-parent">Attach to change</label>
    <select id="at-parent" bind:value={target}>
      {#each choices as c (c.id)}<option value={c.id}>{c.title || c.id} ({c.methodology})</option>{/each}
    </select>
    <p class="hint">
      The change becomes a sub-change of the one chosen, which must integrate its methodology: what it committed is rebased onto that change,
      and it merges into it rather than into main.
    </p>
    <div class="row">
      <button type="submit" class="primary small" disabled={busy || !target}>Attach</button>
      <button type="button" class="small" onclick={oncancel}>Cancel</button>
    </div>
  {/if}
  {#if error}<div class="alert small">{error}</div>{/if}
</form>

<style>
  .attach {
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
  .hint {
    margin: 0;
    font-size: 0.85em;
    color: var(--muted);
  }
  .alert.small {
    font-size: 0.88em;
  }
</style>
