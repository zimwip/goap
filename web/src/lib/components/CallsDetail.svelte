<script lang="ts">
  // Model / tool calls of a step or action run: shared by StepsTimeline (a run's steps) and JournalTab (a
  // change's execution journal) — both list the calls of an ExecutionRecord / Step, same shape.
  import { formatDuration, formatInt } from '../api';

  interface Call {
    provider?: string;
    model?: string;
    inputTokens?: number | string;
    outputTokens?: number | string;
    durationMs?: number | string;
    error?: string;
  }
  interface ToolCall {
    name?: string;
    durationMs?: number | string;
    error?: string;
  }

  let { modelCalls = [], toolCalls = [] }: { modelCalls?: Call[]; toolCalls?: ToolCall[] } = $props();
</script>

{#if modelCalls.length}
  <details>
    <summary>Model calls ({modelCalls.length})</summary>
    <table class="calls">
      <thead>
        <tr><th>Provider</th><th>Model</th><th class="num">Input</th><th class="num">Output</th><th class="num">Duration</th><th>Error</th></tr>
      </thead>
      <tbody>
        {#each modelCalls as c, k (k)}
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
{#if toolCalls.length}
  <details>
    <summary>Tool calls ({toolCalls.length})</summary>
    <table class="calls">
      <thead><tr><th>Tool</th><th class="num">Duration</th><th>Error</th></tr></thead>
      <tbody>
        {#each toolCalls as c, k (k)}
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

<style>
  details {
    margin-top: 0.25rem;
  }
  summary {
    cursor: pointer;
    font-size: 0.88rem;
    color: var(--muted);
  }
  .calls {
    margin-top: 0.25rem;
    font-size: 0.9em;
  }
  tr.err td {
    color: var(--danger);
  }
</style>
