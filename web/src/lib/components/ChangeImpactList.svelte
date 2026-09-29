<script lang="ts">
  // The nodes a change acts on (ADR 0024): intent and rationale, the version it
  // starts from, writes and lands as, and their review. A review needs a comment.
  import { graph, errorMessage, type ChangeImpact } from '../api';
  import StatusBadge from './StatusBadge.svelte';

  let {
    changeId,
    nodes,
    closed = false,
    scope = '',
    onchange,
    onopennode,
  }: {
    changeId: string;
    nodes: ChangeImpact[];
    /** the flow the list is the view of ('main' or an option id, ADR 0032 §6); empty: the whole list of the change */
    scope?: string;
    /** the change is applied or abandoned */
    closed?: boolean;
    onchange?: () => void;
    onopennode?: (n: ChangeImpact) => void;
  } = $props();

  let reviewing = $state('');
  let comment = $state('');
  let busy = $state(false);
  let error = $state('');

  const version = (r?: { id?: string; version?: number }) => (r?.id ? `v${r.version}` : '—');
  const scoped = $derived(scope !== '');
  const onOption = $derived(scoped && scope !== 'main');
  // a scoped list holds what its flow sees: its reviews go to that flow; the whole list reviews the main flow only
  const canReview = (n: ChangeImpact) => !closed && (scoped || !n.flow) && !n.superseded && n.review === 'proposed';

  async function review(n: ChangeImpact, accept: boolean) {
    busy = true;
    error = '';
    try {
      await graph.reviewChangeImpact(changeId, n.id ?? '', accept, comment, scoped ? scope : '');
      reviewing = '';
      comment = '';
      onchange?.();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if error}<div class="alert">{error}</div>{/if}
{#if nodes.length}
  <table>
    <thead><tr><th>Node</th><th>Intent</th><th>Why</th><th>Pre</th><th>Post</th><th>Landed</th><th>Review</th></tr></thead>
    <tbody>
      {#each nodes as n (n.id)}
        <tr class:superseded={n.superseded}>
          <td>
            {#if onopennode && (n.pre?.id || n.post?.id)}
              <button type="button" class="link" onclick={() => onopennode?.(n)}><code>{n.key}</code></button>
            {:else}
              <code>{n.key}</code>
            {/if}
            <span class="hint">{n.type}</span>
          </td>
          <td>{n.intent}{#if onOption}{#if n.flow === scope} <span class="origin own" title="declared by this option: it lands only if the option is selected">this option</span>{:else} <span class="origin" title="declared on the main flow: every option sees it">main flow</span>{/if}{:else if n.flow} <span class="hint" title="declared on a flow branch: a candidate until the flow is adopted">candidate</span>{/if}{#if n.superseded} <span class="hint" title="replaced by an adopted flow">superseded</span>{/if}{#if n.recheck} <span class="hint" title="written against a version that is no longer the head">re-check</span>{/if}</td>
          <td>{n.rationale}</td>
          <td>{version(n.pre)}</td>
          <td>{version(n.post)}</td>
          <td>{version(n.landed)}</td>
          <td>
            <StatusBadge status={n.review} />
            {#if n.reviews?.length}<div class="hint">{n.reviews.at(-1)?.by ? `${n.reviews.at(-1)?.by}: ` : ''}{n.reviews.at(-1)?.comment}</div>{/if}
            {#if canReview(n)}
              {#if reviewing === n.id}
                <div class="row">
                  <input type="text" placeholder="Comment (required)" bind:value={comment} aria-label="Review comment for {n.key}" />
                  <button type="button" class="primary" disabled={busy || !comment.trim()} onclick={() => review(n, true)}>Accept</button>
                  <button type="button" disabled={busy || !comment.trim()} onclick={() => review(n, false)}>Reject</button>
                  <button type="button" class="link" onclick={() => ((reviewing = ''), (comment = ''))}>Cancel</button>
                </div>
              {:else}
                <button type="button" class="link" onclick={() => (reviewing = n.id ?? '')}>Review…</button>
              {/if}
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{:else}
  <p class="empty">No change impacts.</p>
{/if}

<style>
  tr.superseded {
    opacity: 0.55;
    text-decoration: line-through;
  }
  .origin {
    font-size: 0.8em;
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 0 6px;
    color: var(--muted);
  }
  .origin.own {
    color: var(--scope, var(--accent));
    border-color: var(--scope, var(--accent));
  }
</style>
