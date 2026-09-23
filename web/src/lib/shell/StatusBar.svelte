<script lang="ts">
  // Status bar: platform health, my running runs, identity,
  // and notifications.
  import Icon from './Icon.svelte';
  import Popover from './Popover.svelte';
  import { openTab } from './tabs.svelte';
  import { health, startHealth, refreshHealth, myActiveRuns, myRunsState, refreshMyRuns } from '../stores/status.svelte';
  import { session } from '../stores/session.svelte';
  import { notifications, markRead, markAllRead, clearNotifications, type Notice } from '../stores/notifications.svelte';
  import { live } from '../stores/live.svelte';
  import { formatTime, shortId, onTokenChange } from '../api';
  import StatusBadge from '../components/StatusBadge.svelte';

  let healthOpen = $state(false);
  let runsOpen = $state(false);
  let bellOpen = $state(false);
  let now = $state(Date.now());

  $effect(() => startHealth());
  $effect(() => {
    void refreshMyRuns();
    const t = setInterval(() => void refreshMyRuns(), 60_000);
    const off = onTokenChange(() => void refreshMyRuns());
    return () => {
      clearInterval(t);
      off();
    };
  });
  // Elapsed-time clock (only while the list is open).
  $effect(() => {
    if (!runsOpen) return;
    now = Date.now();
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const runs = $derived(myActiveRuns());
  const running = $derived(runs.some((p) => p.status === 'running'));

  const HEALTH: Record<string, string> = {
    ok: 'Platform OK',
    degraded: 'Platform degraded',
    down: 'Platform unavailable',
    unknown: 'Platform status unknown',
  };

  function elapsed(iso: string | undefined): string {
    if (!iso) return '';
    const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
    if (s < 60) return `${s} s`;
    if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
    return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  }

  function openRun(id: string) {
    openTab({ kind: 'run', params: { id } }, { pin: true });
    runsOpen = false;
  }

  function openNotice(n: Notice) {
    markRead(n.id);
    if (n.processId) openTab({ kind: 'run', params: { id: n.processId } }, { pin: true });
    bellOpen = false;
  }
</script>

<footer class="status" aria-label="Status bar">
  <div class="item-wrap">
    <button type="button" class="item health {health.status}" aria-haspopup="dialog" aria-expanded={healthOpen} onclick={() => (healthOpen = !healthOpen)}>
      <span class="dot" aria-hidden="true"></span>{HEALTH[health.status] ?? health.status}
    </button>
    <Popover bind:open={healthOpen} label="Platform status">
      <div class="pop-head">
        <strong>{HEALTH[health.status] ?? health.status}</strong>
        <span class="grow"></span>
        <button type="button" class="small ghost" onclick={() => refreshHealth()} aria-label="Refresh"><Icon name="refresh" size={12} /></button>
      </div>
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
        <p class="pad hint">{health.error || 'No service reported.'}</p>
      {/if}
      <p class="pad hint">Checked at {formatTime(health.checkedAt)} (every 15 s).</p>
    </Popover>
  </div>

  <span class="item muted stream" title={live.error || 'Event stream'}>
    <Icon name="radio" size={12} />{live.status === 'retrying' ? 'stream: reconnecting' : live.status === 'stopped' ? 'stream stopped' : 'stream live'}
  </span>

  <span class="grow"></span>

  <div class="item-wrap">
    <button type="button" class="item" aria-haspopup="dialog" aria-expanded={runsOpen} onclick={() => (runsOpen = !runsOpen)} title="My running runs">
      {#if running}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="play" size={12} />{/if}
      {runs.length ? `${runs.length} run${runs.length > 1 ? 's' : ''} in progress` : 'No runs in progress'}
    </button>
    <Popover bind:open={runsOpen} label="My running runs" width="380px">
      <div class="pop-head"><strong>My running runs</strong></div>
      {#if myRunsState.error}<p class="pad alert">{myRunsState.error}</p>{/if}
      {#if runs.length}
        <ul class="list">
          {#each runs as p (p.id)}
            <li>
              <button type="button" class="row-btn" onclick={() => openRun(p.id ?? '')}>
                <span class="main">
                  <strong>{p.agent || p.title || shortId(p.id)}</strong>
                  <span class="hint">{p.goal ? `goal ${p.goal}` : 'goal to be determined'}</span>
                </span>
                <StatusBadge status={p.status} />
                <span class="hint el">{elapsed(p.createdAt)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="pad hint">No runs in progress.</p>
      {/if}
    </Popover>
  </div>

  <span class="item muted" title={session.principal?.roles?.length ? `Roles: ${session.principal.roles.join(', ')}` : ''}>
    <Icon name="user" size={12} />{session.principal?.subject || (session.hasToken ? 'token configured' : 'anonymous')}{session.principal?.org
      ? ` · ${session.principal.org}`
      : ''}
  </span>

  <div class="item-wrap">
    <button
      type="button"
      class="item bell"
      aria-haspopup="dialog"
      aria-expanded={bellOpen}
      aria-label={`Notifications${notifications.unread ? ` (${notifications.unread} unread)` : ''}`}
      onclick={() => (bellOpen = !bellOpen)}
    >
      <Icon name="bell" size={13} />
      {#if notifications.unread}<span class="count">{notifications.unread}</span>{/if}
    </button>
    <Popover bind:open={bellOpen} label="Notifications" align="right" width="380px">
      <div class="pop-head">
        <strong>Notifications</strong>
        <span class="grow"></span>
        <button type="button" class="small ghost" disabled={!notifications.unread} onclick={markAllRead}>Mark all read</button>
        <button type="button" class="small ghost" disabled={!notifications.items.length} onclick={clearNotifications}>Clear</button>
      </div>
      {#if notifications.items.length}
        <ul class="list">
          {#each notifications.items as n (n.id)}
            <li class:unread={!n.read}>
              <button type="button" class="row-btn" onclick={() => openNotice(n)}>
                <span class="tone {n.tone}" aria-hidden="true"></span>
                <span class="main">
                  <strong>{n.title}</strong>
                  <span class="txt">{n.text}</span>
                </span>
                <span class="hint el">{formatTime(n.time)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {:else}
        <p class="pad hint">No notifications.</p>
      {/if}
    </Popover>
  </div>
</footer>

<style>
  .status {
    display: flex;
    align-items: stretch;
    height: 22px;
    flex: none;
    background: var(--accent);
    color: var(--accent-text);
    font-size: 12px;
    padding: 0 0.3rem;
    position: relative;
    z-index: 30;
  }
  .item-wrap {
    position: relative;
    display: flex;
  }
  .item {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0 0.5rem;
    min-height: 0;
    border: none;
    border-radius: 0;
    background: transparent;
    color: inherit;
    font-size: 12px;
    font-weight: 500;
    white-space: nowrap;
  }
  button.item:hover:not(:disabled) {
    background: rgb(255 255 255 / 0.15);
  }
  button.item:focus-visible {
    outline: 1px solid currentColor;
    outline-offset: -2px;
  }
  .muted {
    opacity: 0.9;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #9aa;
    box-shadow: 0 0 0 1px rgb(255 255 255 / 0.6);
  }
  .health.ok .dot {
    background: #40c057;
  }
  .health.degraded .dot {
    background: #fab005;
  }
  .health.down .dot {
    background: #fa5252;
  }
  .spin {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    border: 2px solid currentColor;
    border-right-color: transparent;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .bell {
    position: relative;
  }
  .count {
    min-width: 15px;
    height: 15px;
    border-radius: 8px;
    padding: 0 4px;
    background: var(--danger);
    color: #fff;
    font-size: 10px;
    font-weight: 700;
    line-height: 15px;
    text-align: center;
  }
  .pop-head {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.45rem 0.6rem;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--surface);
  }
  .pad {
    padding: 0.4rem 0.6rem;
    margin: 0;
  }
  .svc td {
    padding: 0.2rem 0.6rem;
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
  .list {
    list-style: none;
    margin: 0;
    padding: 0.2rem 0;
  }
  .row-btn {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    border: none;
    border-radius: 0;
    background: none;
    text-align: left;
    font-weight: 400;
    padding: 0.3rem 0.6rem;
    min-height: 0;
  }
  .row-btn:hover:not(:disabled) {
    background: var(--hover);
  }
  .main {
    flex: 1;
    min-width: 0;
    display: grid;
  }
  .main .txt,
  .main .hint {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--muted);
  }
  .unread .main strong::after {
    content: ' ●';
    color: var(--accent);
  }
  .el {
    white-space: nowrap;
  }
  .tone {
    width: 4px;
    align-self: stretch;
    border-radius: 2px;
    background: var(--info);
  }
  .tone.ok {
    background: var(--ok);
  }
  .tone.error {
    background: var(--danger);
  }
  .tone.warn {
    background: var(--warn);
  }
  @media (max-width: 900px) {
    .stream {
      display: none;
    }
  }
</style>
