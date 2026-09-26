<script lang="ts">
  // The nodes a change acts on (ADR 0024): intent and rationale, the version it
  // starts from, writes and lands as, and their review. A review needs a comment.
  import { graph, errorMessage, type ChangeImpact } from '../api';
  import StatusBadge from './StatusBadge.svelte';

  let {
    changeId,
    nodes,
    closed = false,
    onchange,
    onopennode,
  }: {
    changeId: string;
    nodes: ChangeImpact[];
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
  const canReview = (n: ChangeImpact) => !closed && !n.flow && !n.superseded && n.review === 'proposed';

  async function review(n: ChangeImpact, accept: boolean) {
    busy = true;
    error = '';
    try {
      await graph.reviewChangeImpact(changeId, n.id ?? '', accept, comment);
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
          <td>{n.intent}{#if n.flow} <span class="hint" title="declared on a flow branch: a candidate until the flow is adopted">candidate</span>{/if}{#if n.superseded} <span class="hint" title="replaced by an adopted flow">superseded</span>{/if}{#if n.recheck} <span class="hint" title="written against a version that is no longer the head">re-check</span>{/if}</td>
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
</style>
