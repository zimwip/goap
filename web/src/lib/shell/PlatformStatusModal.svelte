<script lang="ts">
  // Platform status: event stream + service health, in one modal reached from
  // the header's live indicator. Mounted once at the shell root.
  import Icon from './Icon.svelte';
  import { platformStatusState, closePlatformStatus } from './platformStatusState.svelte';
  import { health, refreshHealth } from '../stores/status.svelte';
  import { live } from '../stores/live.svelte';
  import { formatTime } from '../api';

  const STREAM_LABEL: Record<string, string> = {
    open: 'Connected',
    connecting: 'Waiting for events',
    retrying: 'Interrupted — reconnecting…',
    stopped: 'Stopped',
  };

  const HEALTH: Record<string, string> = {
    ok: 'Platform OK',
    degraded: 'Platform degraded',
    down: 'Platform unavailable',
    unknown: 'Platform status unknown',
  };

  let dialog = $state<HTMLDivElement>();

  $effect(() => {
    if (platformStatusState.open) queueMicrotask(() => dialog?.focus());
  });
</script>

<svelte:window onkeydown={(e) => platformStatusState.open && e.key === 'Escape' && closePlatformStatus()} />

{#if platformStatusState.open}
  <div class="backdrop" role="presentation" onmousedown={closePlatformStatus}>
    <div
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-label="Platform status"
      tabindex="-1"
      bind:this={dialog}
      onmousedown={(e) => e.stopPropagation()}
    >
      <div class="head">
        <h2>Platform status</h2>
        <button type="button" class="ghost small" aria-label="Close" onclick={closePlatformStatus}><Icon name="x" size={14} /></button>
      </div>

      <section class="row-item">
        <span class="led {live.status}" aria-hidden="true"></span>
        <div class="grow">
          <strong>Live event stream</strong>
          <p class="hint">{STREAM_LABEL[live.status] ?? live.status}{live.error ? ` — ${live.error}` : ''}</p>
        </div>
      </section>

      <section class="row-item">
        <span class="dot {health.status}" aria-hidden="true"></span>
        <div class="grow">
          <strong>{HEALTH[health.status] ?? health.status}</strong>
          <p class="hint">Checked at {formatTime(health.checkedAt)} (every 15 s).</p>
        </div>
        <button type="button" class="small ghost" onclick={() => refreshHealth()} aria-label="Refresh"><Icon name="refresh" size={12} /></button>
      </section>

      {#if health.data?.services?.length}
        <table class="svc">
          <tbody>
            {#each health.data.services as s (s.name)}
              <tr>
                <td><span class="sdot {s.status}" aria-hidden="true"></span>{s.name}</td>
                <td class="num">{s.latencyMs !== undefined ? `${s.latencyMs} ms` : ''}</td>
                <td class="err">{s.error ?? ''}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="hint">{health.error || 'No service reported.'}</p>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 96;
    display: grid;
    place-items: center;
    background: rgba(0, 0, 0, 0.4);
  }
  .dialog {
    width: min(480px, calc(100vw - 28px));
    max-height: calc(100vh - 40px);
    overflow: auto;
    display: grid;
    gap: 0.7rem;
    padding: 1rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .row-item {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .row-item strong {
    font-size: 0.95em;
  }
  .row-item p {
    margin: 0.1rem 0 0;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .led {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--muted);
    flex: none;
  }
  .led.open,
  .led.connecting {
    background: var(--ok);
  }
  .led.retrying {
    background: var(--warn);
  }
  .dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: #9aa;
    flex: none;
  }
  .dot.ok {
    background: #40c057;
  }
  .dot.degraded {
    background: #fab005;
  }
  .dot.down {
    background: #fa5252;
  }
  .svc {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9em;
  }
  .svc td {
    padding: 0.3rem 0.4rem;
    border-top: 1px solid var(--border);
  }
  .sdot {
    display: inline-block;
    width: 7px;
    height: 7px;
    border-radius: 50%;
    margin-right: 0.4rem;
    background: var(--muted);
  }
  .sdot.up {
    background: var(--ok);
  }
  .sdot.down {
    background: var(--danger);
  }
  .err {
    color: var(--danger);
    font-size: 0.9em;
  }
</style>
