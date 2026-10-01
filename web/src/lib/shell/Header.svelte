<script lang="ts">
  // Header: product, search / command palette, stream status,
  // user menu (profile, sign-out in local mode, access token with hs256, theme: a shortcut to the
  // preference, saved as it changes).
  import Icon, { type IconName } from './Icon.svelte';
  import { COMMANDS } from './commands';
  import { tabsState, openTab, activate } from './tabs.svelte';
  import { editorView } from './registry';
  import { focusRequests } from './workbench.svelte';
  import { getToken, setToken, shortId, logout } from '../api';
  import { authState, signsInLocally } from '../stores/auth.svelte';
  import { notify } from './workbench.svelte';
  import { openSearch } from './searchOverlay.svelte.ts';
  import { session, refreshIdentity } from '../stores/session.svelte';
  import { methodologies, baselines, changes } from '../stores/catalog.svelte';
  import { viewBaseline } from '../stores/baselineTool.svelte';
  import { live, processes } from '../stores/live.svelte';
  import { health, startHealth } from '../stores/status.svelte';
  import { openPlatformStatus } from './platformStatusState.svelte';
  import { openSettings } from './settingsState.svelte';
  import type { Theme } from './layout.svelte';
  import { prefs, editPrefs } from '../stores/preferences.svelte';
  import { openUsage } from './usageState.svelte';
  import { notifications, markRead, markAllRead, clearNotifications, type Notice } from '../stores/notifications.svelte';
  import { formatTime } from '../api';
  import Popover from './Popover.svelte';
  import ProjectSelector from './ProjectSelector.svelte';

  $effect(() => startHealth());

  // Worst of the event stream and the platform health, for one combined dot.
  // "connecting" is the normal state while the stream handshakes (same as the
  // status modal, which shows it green too) — only "retrying" is a problem.
  const combined = $derived.by((): 'ok' | 'warn' | 'down' => {
    if (health.status === 'down' || live.status === 'stopped') return 'down';
    if (health.status === 'degraded' || live.status === 'retrying') return 'warn';
    return 'ok';
  });

  // --- search -------------------------------------------------------------------

  interface Result {
    key: string;
    label: string;
    detail: string;
    icon: IconName;
    run: () => void;
  }

  let query = $state('');
  let open = $state(false);
  let cursor = $state(0);
  let input: HTMLInputElement;

  function match(text: string, q: string): boolean {
    const t = text.toLowerCase();
    return q.split(/\s+/).every((w) => t.includes(w));
  }

  const results = $derived.by((): Result[] => {
    const raw = query.trim();
    if (raw.startsWith('>')) {
      const q = raw.slice(1).trim().toLowerCase();
      return COMMANDS.filter((c) => !q || match(c.label, q)).map((c) => ({
        key: `cmd:${c.id}`,
        label: c.label,
        detail: c.shortcut ?? 'command',
        icon: c.icon ?? 'code',
        run: c.run,
      }));
    }
    const q = raw.toLowerCase();
    const out: Result[] = [];
    for (const t of tabsState.tabs) {
      const v = editorView(t.kind);
      const label = v?.tabTitle(t) ?? t.id;
      if (!q || match(`${label} ${v?.title ?? ''}`, q))
        out.push({ key: `tab:${t.id}`, label, detail: 'open tab', icon: v?.tabIcon?.(t) ?? v?.icon ?? 'file', run: () => activate(t.id) });
    }
    if (!q) return out.slice(0, 30);
    for (const m of methodologies.items) {
      const label = `${m.name} v${m.version}`;
      if (match(`${label} ${m.description ?? ''} ${m.status}`, q))
        out.push({
          key: `m:${label}`,
          label,
          detail: `methodology · ${m.status}`,
          icon: 'book',
          run: () => openTab({ kind: 'methodology', params: { name: m.name ?? '', version: m.version ?? '' } }),
        });
      for (const a of m.agents ?? [])
        if (match(`${a.name} ${a.description ?? ''}`, q))
          out.push({
            key: `a:${label}:${a.name}`,
            label: a.name ?? '',
            detail: `agent · ${label}`,
            icon: 'bot',
            run: () =>
              openTab({ kind: 'agent', params: { m: m.name ?? '', v: m.version ?? '', uid: a.name ?? '', name: a.name ?? '' } }),
          });
    }
    for (const p of processes.values()) {
      const label = p.title || `Run ${shortId(p.id)}`;
      if (match(`${label} ${p.id} ${p.agent ?? ''} ${p.goal ?? ''} ${p.methodology ?? ''}`, q))
        out.push({
          key: `p:${p.id}`,
          label,
          detail: `run · ${p.status}${p.agent ? ` · ${p.agent}` : ''}`,
          icon: 'runs',
          run: () => openTab({ kind: 'run', params: { id: p.id ?? '' } }),
        });
    }
    for (const b of baselines.items)
      if (match(`${b.name ?? ''} ${b.id}`, q))
        out.push({
          key: `b:${b.id}`,
          label: b.name || shortId(b.id),
          detail: 'baseline',
          icon: 'database',
          run: () => void viewBaseline(b.id ?? '', b.namespace ?? ''),
        });
    for (const c of changes.items)
      if (match(`${c.title ?? ''} ${c.id} ${c.intent ?? ''}`, q))
        out.push({
          key: `c:${c.id}`,
          label: c.title || shortId(c.id),
          detail: `change · ${c.status}`,
          icon: 'diff',
          run: () => openTab({ kind: 'change', params: { id: c.id ?? '' } }),
        });
    // nodes of the graph: the search overlay (index, facets)
    out.push({
      key: 'search:nodes',
      label: `Search nodes for “${raw}”…`,
      detail: 'full text + semantic, facets',
      icon: 'search',
      run: () => openSearch(raw),
    });
    return out.slice(0, 41);
  });

  $effect(() => {
    void query;
    cursor = 0;
  });

  $effect(() => {
    if (focusRequests.search) {
      input?.focus();
      input?.select();
      open = true;
    }
  });

  function choose(r: Result | undefined) {
    if (!r) return;
    r.run();
    query = '';
    open = false;
    input.blur();
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') cursor = Math.min(cursor + 1, results.length - 1);
    else if (e.key === 'ArrowUp') cursor = Math.max(cursor - 1, 0);
    else if (e.key === 'Enter') choose(results[cursor]);
    else if (e.key === 'Escape') {
      query = '';
      open = false;
      input.blur();
    } else return;
    e.preventDefault();
  }

  // --- user ---------------------------------------------------------------------

  let menuOpen = $state(false);
  let bellOpen = $state(false);
  let tokenDraft = $state('');
  const principal = $derived(session.principal);
  const hasToken = $derived(session.hasToken);

  $effect(() => {
    if (!session.loaded) void refreshIdentity();
  });

  function toggleMenu() {
    menuOpen = !menuOpen;
    tokenDraft = getToken() ?? '';
  }

  function saveToken(e: SubmitEvent) {
    e.preventDefault();
    setToken(tokenDraft.trim() || null);
    menuOpen = false;
  }

  function clearToken() {
    setToken(null);
    tokenDraft = '';
  }

  // Local sign-in (ADR 0042): the menu offers the profile and signing out; a pasted token is only for a
  // deployment whose tokens are issued elsewhere (hs256), and "none" needs neither.
  const local = $derived(signsInLocally(authState.mode));

  function openProfile() {
    menuOpen = false;
    const subject = principal?.subject;
    if (subject) openTab({ kind: 'user', params: { key: `USR:${subject}` } }, { pin: true });
  }

  async function signOut() {
    menuOpen = false;
    await logout();
    notify('Signed out.', 'ok');
  }

  function openNotice(n: Notice) {
    markRead(n.id);
    if (n.processId) openTab({ kind: 'run', params: { id: n.processId } }, { pin: true });
    bellOpen = false;
  }

  const STREAM_LABEL: Record<string, string> = {
    open: 'Live stream connected',
    connecting: 'Live stream: waiting for events',
    retrying: 'Stream interrupted — reconnecting…',
    stopped: 'Stream stopped',
  };

  function outside(node: HTMLElement) {
    const handler = (e: MouseEvent) => {
      if (!node.contains(e.target as Node)) menuOpen = false;
    };
    document.addEventListener('mousedown', handler);
    return { destroy: () => document.removeEventListener('mousedown', handler) };
  }
</script>

<header class="header">
  <div class="brand"><span class="logo" aria-hidden="true">◆</span> GOAP</div>
  <ProjectSelector />

  <div class="search">
    <span class="sicon"><Icon name="search" size={14} /></span>
    <input
      bind:this={input}
      type="search"
      role="combobox"
      aria-expanded={open && results.length > 0}
      aria-controls="search-results"
      aria-autocomplete="list"
      aria-label="Search for an object or a command"
      placeholder="Search (Ctrl+P) — '&gt;' for commands"
      bind:value={query}
      onfocus={() => (open = true)}
      onblur={() => setTimeout(() => (open = false), 150)}
      onkeydown={keydown}
      data-no-pin
    />
    {#if open && results.length}
      <ul class="results" id="search-results" role="listbox">
        {#each results as r, i (r.key)}
          <li
            role="option"
            aria-selected={i === cursor}
            class:on={i === cursor}
            onmousedown={(e) => {
              e.preventDefault();
              choose(r);
            }}
            onmousemove={() => (cursor = i)}
          >
            <Icon name={r.icon} size={14} />
            <span class="rl">{r.label}</span>
            <span class="rd">{r.detail}</span>
          </li>
        {/each}
      </ul>
    {/if}
  </div>

  <div class="right">
    <button
      type="button"
      class="stream {combined}"
      title={`${STREAM_LABEL[live.status]}${live.error ? ` : ${live.error}` : ''}`}
      aria-haspopup="dialog"
      onclick={openPlatformStatus}
    >
      <span class="led" aria-hidden="true"></span>
      <span class="sl">{live.status === 'retrying' ? 'reconnecting' : live.status === 'stopped' ? 'offline' : 'live'}</span>
    </button>

    <div class="item-wrap">
      <button
        type="button"
        class="bell"
        aria-haspopup="dialog"
        aria-expanded={bellOpen}
        aria-label={`Notifications${notifications.unread ? ` (${notifications.unread} unread)` : ''}`}
        onclick={() => (bellOpen = !bellOpen)}
      >
        <Icon name="bell" size={15} />
        {#if notifications.unread}<span class="count">{notifications.unread}</span>{/if}
      </button>
      <Popover bind:open={bellOpen} label="Notifications" align="right" placement="below" width="380px">
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

    <div class="user" use:outside>
      <button type="button" class="ghost ubtn" aria-haspopup="true" aria-expanded={menuOpen} onclick={toggleMenu}>
        <Icon name="user" size={15} />
        <span class="uname">{principal?.subject || (hasToken ? 'Token configured' : 'Anonymous')}</span>
      </button>
      {#if menuOpen}
        <div class="menu" role="dialog" aria-label="User">
          <p class="hint">
            {#if principal?.subject}
              Signed in: <strong>{principal.subject}</strong>{principal.org ? ` · ${principal.org}` : ''}
              {#if principal.roles?.length}<br />Roles: {principal.roles.join(', ')}{/if}
            {:else}
              {session.error || 'No identity.'}
            {/if}
          </p>
          {#if authState.mode === 'hs256'}
            <form onsubmit={saveToken}>
              <label for="token">Access token (Bearer)</label>
              <input id="token" type="password" bind:value={tokenDraft} autocomplete="off" placeholder="token…" />
              <div class="row" style="margin-top: 0.4rem">
                <button class="small primary" type="submit">Save</button>
                {#if hasToken}<button class="small" type="button" onclick={clearToken}>Remove</button>{/if}
              </div>
            </form>
          {/if}
          <div class="theme">
            <label for="theme">Theme</label>
            <select id="theme" value={prefs.values.theme} onchange={(e) => void editPrefs({ theme: e.currentTarget.value as Theme })}>
              <option value="auto">System</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          </div>
          <div class="menu-actions">
            <button
              type="button"
              class="small settings-btn"
              onclick={() => {
                menuOpen = false;
                openUsage();
              }}
            >
              <Icon name="coins" size={13} />Token usage
            </button>
            <button
              type="button"
              class="small settings-btn"
              onclick={() => {
                menuOpen = false;
                openSettings();
              }}
            >
              <Icon name="settings" size={13} />Settings
            </button>
          </div>
          {#if principal?.subject}
            <div class="menu-actions">
              <button type="button" class="small settings-btn" onclick={openProfile}><Icon name="user" size={13} />My profile</button>
              {#if local && hasToken}
                <button type="button" class="small settings-btn" onclick={() => void signOut()}><Icon name="logout" size={13} />Log out</button>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    </div>
  </div>
</header>

<style>
  .header {
    display: flex;
    align-items: center;
    gap: 0.8rem;
    height: 46px;
    padding: 0 0.6rem 0 0.8rem;
    flex: none;
    background: var(--chrome-2);
    border-bottom: 1px solid var(--border);
  }
  .brand {
    font-weight: 800;
    letter-spacing: 0.06em;
    white-space: nowrap;
  }
  .logo {
    color: var(--accent);
  }
  .search {
    position: relative;
    flex: 1;
    max-width: 560px;
    margin: 0 auto;
  }
  .sicon {
    position: absolute;
    left: 0.5rem;
    top: 50%;
    transform: translateY(-50%);
    color: var(--muted);
    pointer-events: none;
  }
  .search input {
    padding-left: 1.8rem;
    background: var(--surface);
    height: 30px;
  }
  .results {
    position: absolute;
    z-index: 50;
    top: calc(100% + 4px);
    left: 0;
    right: 0;
    max-height: 60vh;
    overflow: auto;
    margin: 0;
    padding: 4px;
    list-style: none;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .results li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0.5rem;
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .results li.on {
    background: var(--accent-soft);
  }
  .rl {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rd {
    color: var(--muted);
    font-size: 0.85rem;
    white-space: nowrap;
  }
  .right {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .stream {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0 0.3rem;
    min-height: 0;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--muted);
    white-space: nowrap;
  }
  .stream:hover {
    background: var(--hover);
  }
  .led {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--muted);
  }
  .stream.ok .led {
    background: var(--ok);
  }
  .stream.warn .led {
    background: var(--warn);
  }
  .stream.down .led {
    background: var(--danger);
  }
  .item-wrap {
    position: relative;
    display: flex;
  }
  .bell {
    position: relative;
    display: inline-flex;
    align-items: center;
    padding: 0.35rem;
    min-height: 0;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: inherit;
  }
  .bell:hover {
    background: var(--hover);
  }
  .count {
    position: absolute;
    top: 0;
    right: 0;
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
  .main .txt {
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
  .user {
    position: relative;
  }
  .ubtn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-weight: 500;
    max-width: 220px;
  }
  .uname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .menu {
    position: absolute;
    z-index: 60;
    right: 0;
    top: calc(100% + 4px);
    width: 290px;
    padding: 0.7rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  .theme {
    margin-top: 0.7rem;
  }
  .menu-actions {
    display: flex;
    gap: 0.4rem;
    margin-top: 0.7rem;
  }
  .settings-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.4rem;
    flex: 1;
  }
  @media (max-width: 900px) {
    .sl,
    .uname {
      display: none;
    }
  }
</style>
