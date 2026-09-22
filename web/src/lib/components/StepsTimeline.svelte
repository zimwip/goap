<script lang="ts">
  import { formatDate, type Step } from '../api';

  let { steps = [] }: { steps?: Step[] } = $props();

  function duration(s: Step): string {
    if (!s.startedAt || !s.endedAt) return '';
    const ms = new Date(s.endedAt).getTime() - new Date(s.startedAt).getTime();
    if (!Number.isFinite(ms) || ms < 0) return '';
    return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
  }

  function state(s: Step): 'error' | 'ok' | 'partial' | 'pending' {
    if (s.error) return 'error';
    if (!s.endedAt) return 'pending';
    return s.effectsMet ? 'ok' : 'partial';
  }

  const LABEL = { error: 'erreur', ok: 'effets atteints', partial: 'effets non atteints', pending: 'en cours' };
</script>

{#if steps.length}
  <ol class="timeline">
    {#each steps as s, i (s.index ?? i)}
      {@const st = state(s)}
      <li class={st}>
        <div class="dot" aria-hidden="true"></div>
        <div class="body">
          <div class="row">
            <span class="idx">#{(s.index ?? i) + 1}</span>
            <code class="action">{s.action}</code>
            <span class="st">{LABEL[st]}</span>
            <span class="grow"></span>
            {#if s.approvedBy}<span class="hint">décidé par {s.approvedBy}</span>{/if}
            {#if s.items?.length}<span class="hint">{s.items.length} item{s.items.length > 1 ? 's' : ''}</span>{/if}
            <span class="hint" title={formatDate(s.startedAt)}>{duration(s)}</span>
          </div>
          {#if s.error}
            <pre class="error">{s.error}</pre>
          {/if}
          {#if s.output}
            <details>
              <summary>Sortie</summary>
              <pre>{s.output}</pre>
            </details>
          {/if}
        </div>
      </li>
    {/each}
  </ol>
{:else}
  <p class="empty">Aucune étape exécutée.</p>
{/if}

<style>
  .timeline {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: grid;
    grid-template-columns: 1rem 1fr;
    gap: 0.7rem;
    position: relative;
    padding-bottom: 0.9rem;
  }
  li:not(:last-child)::before {
    content: '';
    position: absolute;
    left: calc(0.5rem - 1px);
    top: 1.1rem;
    bottom: 0;
    width: 2px;
    background: var(--border);
  }
  .dot {
    width: 0.75rem;
    height: 0.75rem;
    margin: 0.35rem 0 0 0.125rem;
    border-radius: 50%;
    background: var(--muted);
  }
  .ok .dot {
    background: var(--ok);
  }
  .partial .dot {
    background: var(--warn);
  }
  .error .dot {
    background: var(--danger);
  }
  .pending .dot {
    background: var(--accent);
  }
  .body {
    min-width: 0;
  }
  .row {
    gap: 0.5rem;
  }
  .idx {
    color: var(--muted);
    font-size: 0.8rem;
  }
  .action {
    font-weight: 600;
  }
  .st {
    font-size: 0.78rem;
    color: var(--muted);
  }
  .ok .st {
    color: var(--ok);
  }
  .partial .st {
    color: var(--warn);
  }
  .error .st {
    color: var(--danger);
  }
  pre.error {
    margin-top: 0.35rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  details {
    margin-top: 0.3rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.85rem;
    color: var(--muted);
  }
  details pre {
    margin-top: 0.35rem;
    max-height: 22rem;
  }
</style>
