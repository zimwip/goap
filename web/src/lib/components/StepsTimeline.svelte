<script lang="ts">
  import { formatDate, formatDuration, formatInt, formatTime, int, shortId, type Step } from '../api';

  let {
    steps = [],
    onopenprocess,
    liveLogs = [],
  }: {
    steps?: Step[];
    /** ouverture d'un processus enfant (sous-agent) */
    onopenprocess?: (id: string) => void;
    /** journaux reçus en direct, rattachés à leur étape */
    liveLogs?: { time?: string; level?: string; message?: string; step?: number }[];
  } = $props();

  function duration(s: Step): string {
    if (!s.startedAt || !s.endedAt) return '';
    const ms = new Date(s.endedAt).getTime() - new Date(s.startedAt).getTime();
    if (!Number.isFinite(ms) || ms < 0) return '';
    return formatDuration(ms);
  }

  function state(s: Step): 'error' | 'ok' | 'partial' | 'pending' {
    if (s.error) return 'error';
    if (!s.endedAt) return 'pending';
    return s.effectsMet ? 'ok' : 'partial';
  }

  const LABEL = { error: 'erreur', ok: 'effets atteints', partial: 'effets non atteints', pending: 'en cours' };

  function logsOf(s: Step, i: number) {
    const idx = s.index ?? i;
    const own = s.logs ?? [];
    const seen = new Set(own.map((l) => `${l.time}|${l.message}`));
    const extra = liveLogs.filter((l) => l.step === idx && !seen.has(`${l.time}|${l.message}`));
    return [...own, ...extra];
  }
</script>

{#if steps.length}
  <ol class="timeline">
    {#each steps as s, i (s.index ?? i)}
      {@const st = state(s)}
      {@const logs = logsOf(s, i)}
      {@const tokensIn = int(s.usage?.inputTokens)}
      {@const tokensOut = int(s.usage?.outputTokens)}
      <li class={st}>
        <div class="dot" aria-hidden="true"></div>
        <div class="body">
          <div class="row head">
            <span class="idx">#{(s.index ?? i) + 1}</span>
            <code class="action">{s.action}</code>
            <span class="st">{LABEL[st]}</span>
            {#if s.sandbox}<span class="tag" title="Sandbox d'exécution">⧉ {s.sandbox}</span>{/if}
            <span class="grow"></span>
            {#if tokensIn || tokensOut}
              <span class="usage" title="Tokens entrée / sortie">{formatInt(tokensIn)} → {formatInt(tokensOut)} tok</span>
            {/if}
            {#if s.usage?.llmCalls}<span class="hint">{s.usage.llmCalls} LLM</span>{/if}
            {#if s.usage?.toolCalls}<span class="hint">{s.usage.toolCalls} outil{s.usage.toolCalls > 1 ? 's' : ''}</span>{/if}
            {#if s.approvedBy}<span class="hint">décidé par {s.approvedBy}</span>{/if}
            {#if s.items?.length}<span class="hint">{s.items.length} item{s.items.length > 1 ? 's' : ''}</span>{/if}
            <span class="hint" title={formatDate(s.startedAt)}>{duration(s)}</span>
          </div>
          {#if s.error}
            <pre class="error">{s.error}</pre>
          {/if}
          {#if s.childProcessIds?.length}
            <div class="children">
              Sous-agents :
              {#each s.childProcessIds as c (c)}
                <button type="button" class="link mono" onclick={() => onopenprocess?.(c)}>{shortId(c)}</button>
              {/each}
            </div>
          {/if}
          {#if s.llmCalls?.length}
            <details>
              <summary>Appels LLM ({s.llmCalls.length})</summary>
              <table class="calls">
                <thead>
                  <tr><th>Fournisseur</th><th>Modèle</th><th class="num">Entrée</th><th class="num">Sortie</th><th class="num">Durée</th><th>Erreur</th></tr>
                </thead>
                <tbody>
                  {#each s.llmCalls as c, k (k)}
                    <tr class:err={!!c.error}>
                      <td>{c.provider}</td>
                      <td><code>{c.model}</code></td>
                      <td class="num">{formatInt(c.inputTokens)}</td>
                      <td class="num">{formatInt(c.outputTokens)}</td>
                      <td class="num">{formatDuration(c.durationMs)}</td>
                      <td>{c.error ?? ''}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </details>
          {/if}
          {#if s.toolCalls?.length}
            <details>
              <summary>Appels d'outils ({s.toolCalls.length})</summary>
              <table class="calls">
                <thead><tr><th>Outil</th><th class="num">Durée</th><th>Erreur</th></tr></thead>
                <tbody>
                  {#each s.toolCalls as c, k (k)}
                    <tr class:err={!!c.error}>
                      <td><code>{c.name}</code></td>
                      <td class="num">{formatDuration(c.durationMs)}</td>
                      <td>{c.error ?? ''}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </details>
          {/if}
          {#if logs.length}
            <details open={st === 'pending' || st === 'error'}>
              <summary>Journal ({logs.length})</summary>
              <ol class="logs">
                {#each logs as l, k (k)}
                  <li class="lvl-{l.level || 'info'}">
                    <span class="t">{formatTime(l.time)}</span>
                    <span class="lv">{l.level || 'info'}</span>
                    <span class="m">{l.message}</span>
                  </li>
                {/each}
              </ol>
            </details>
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
    gap: 0.6rem;
    position: relative;
    padding-bottom: 0.7rem;
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
    width: 0.7rem;
    height: 0.7rem;
    margin: 0.3rem 0 0 0.15rem;
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
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.3;
    }
  }
  .body {
    min-width: 0;
  }
  .head {
    gap: 0.45rem;
  }
  .idx {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .action {
    font-weight: 600;
  }
  .st {
    font-size: 0.82rem;
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
  .tag {
    font-size: 0.78rem;
    font-family: var(--mono);
    background: var(--info-soft);
    color: var(--info);
    border-radius: 3px;
    padding: 0 0.3rem;
  }
  .usage {
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }
  pre.error {
    margin-top: 0.3rem;
    color: var(--danger);
    background: var(--danger-soft);
    border-color: transparent;
  }
  .children {
    margin-top: 0.25rem;
    font-size: 0.9em;
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
    align-items: baseline;
  }
  details {
    margin-top: 0.25rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.88rem;
    color: var(--muted);
  }
  details pre {
    margin-top: 0.3rem;
    max-height: 22rem;
  }
  .calls {
    margin-top: 0.25rem;
    font-size: 0.9em;
  }
  tr.err td {
    color: var(--danger);
  }
  .logs {
    list-style: none;
    margin: 0.25rem 0 0;
    padding: 0.3rem 0.5rem;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    font-family: var(--mono);
    font-size: 0.85em;
    max-height: 16rem;
    overflow: auto;
  }
  .logs li {
    display: flex;
    gap: 0.5rem;
    padding: 0;
  }
  .logs li::before {
    display: none;
  }
  .t {
    color: var(--muted);
    flex: none;
  }
  .lv {
    flex: none;
    width: 3.2rem;
    text-transform: uppercase;
    font-size: 0.85em;
    color: var(--muted);
  }
  .lvl-warn .lv,
  .lvl-warn .m {
    color: var(--warn);
  }
  .lvl-error .lv,
  .lvl-error .m {
    color: var(--danger);
  }
  .m {
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
