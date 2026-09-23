<script lang="ts">
  // "Triggers" tool: status of published agents' triggers
  // (ListTriggers) and manual firing (FireTrigger).
  import Icon from '../../shell/Icon.svelte';
  import { engine, errorMessage, formatDate, shortId, type TriggerState } from '../../api';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify, select } from '../../shell/workbench.svelte';
  import { ingestProcess, onLiveEvent } from '../../stores/live.svelte';
  import { methodologySpec } from '../editors/methodologyTabs';
  import { methodologies, refreshMethodologies, latestPublished } from '../../stores/catalog.svelte';

  let triggers = $state<TriggerState[]>([]);
  let loading = $state(false);
  let error = $state('');
  let firing = $state('');
  let filter = $state('');

  async function load() {
    loading = true;
    try {
      triggers = (await engine.listTriggers()).triggers ?? [];
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
    if (!methodologies.loaded) void refreshMethodologies();
    const t = setInterval(() => void load(), 30_000);
    // A process launched by a trigger updates the counters.
    const off = onLiveEvent((e) => {
      if (e.type === 'started' && e.process?.trigger) void load();
    });
    return () => {
      clearInterval(t);
      off();
    };
  });

  const q = $derived(filter.trim().toLowerCase());
  const shown = $derived(
    triggers.filter((t) => !q || `${t.methodology} ${t.agent} ${t.name} ${t.event ?? ''} ${t.description ?? ''}`.toLowerCase().includes(q)),
  );

  const key = (t: TriggerState) => `${t.methodology}/${t.agent}/${t.name}`;

  async function fire(t: TriggerState) {
    firing = key(t);
    try {
      const p = (await engine.fireTrigger(t.methodology ?? '', t.agent ?? '', t.name ?? '')).process;
      if (p?.id) {
        ingestProcess(p);
        openTab({ kind: 'run', params: { id: p.id } }, { pin: true });
      }
      notify(`Trigger "${t.name}" fired.`, 'ok');
      void load();
    } catch (e) {
      notify(errorMessage(e), 'error');
    } finally {
      firing = '';
    }
  }

  function show(t: TriggerState) {
    select({
      title: t.name ?? '',
      subtitle: `Trigger of ${t.methodology} / ${t.agent}`,
      rows: [
        ['Description', t.description ?? ''],
        ['Type', t.type ?? ''],
        ['Event', t.event ?? ''],
        ['Schedule', t.schedule ?? ''],
        ['Enabled', t.enabled ? 'yes' : 'no'],
        ['Runs', String(t.fires ?? 0)],
        ['Last fired', formatDate(t.lastFired)],
        ['Next', formatDate(t.nextFire)],
        ['Last error', t.lastError ?? ''],
        ['Last process', t.lastProcessId ?? ''],
      ],
    });
  }

  function openAgent(t: TriggerState) {
    const m = latestPublished().find((x) => x.name === t.methodology);
    if (m?.version) openTab({ kind: 'agent', params: { m: m.name ?? '', v: m.version, uid: t.agent ?? '', name: t.agent ?? '' } });
    else if (t.methodology) openTab(methodologySpec(t.methodology, ''));
  }
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter triggers" bind:value={filter} data-no-pin />
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if error}<div class="alert small">{error}</div>{/if}
  {#if !loading && !error && !triggers.length}
    <p class="empty pad">No triggers on published agents. Add some in an agent's editor (the "Triggers" section).</p>
  {/if}
  <ul class="list">
    {#each shown as t (key(t))}
      <li class:off={!t.enabled}>
        <div class="line1">
          <button type="button" class="link name" onclick={() => show(t)} title="Details in 'Properties'">{t.name}</button>
          <span class="type">{t.type === 'schedule' ? 'cron' : 'event'}</span>
          <span class="grow"></span>
          <button
            type="button"
            class="small fire"
            disabled={!!firing}
            title="Fire now"
            onclick={() => fire(t)}><Icon name="play" size={11} />{firing === key(t) ? '…' : 'Fire'}</button
          >
        </div>
        <div class="line2">
          <button type="button" class="link" onclick={() => openAgent(t)}>{t.methodology} / {t.agent}</button>
          · <code>{t.type === 'schedule' ? t.schedule : t.event}</code>
          {#if !t.enabled}· <span class="warn">disabled</span>{/if}
        </div>
        <div class="line2">
          {t.fires ?? 0} run{(t.fires ?? 0) > 1 ? 's' : ''}
          {#if t.lastFired}· last {formatDate(t.lastFired)}{/if}
          {#if t.nextFire}· next {formatDate(t.nextFire)}{/if}
          {#if t.lastProcessId}
            · <button type="button" class="link mono" onclick={() => openTab({ kind: 'run', params: { id: t.lastProcessId ?? '' } })}
              >{shortId(t.lastProcessId)}</button
            >
          {/if}
        </div>
        {#if t.lastError}<div class="line2 err" title={t.lastError}>{t.lastError}</div>{/if}
      </li>
    {/each}
  </ul>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
  }
  .tools input {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    padding: 0.35rem 0.7rem;
    border-bottom: 1px solid var(--border);
  }
  .list li.off {
    opacity: 0.7;
  }
  .line1 {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .name {
    font-weight: 600;
  }
  .type {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .fire {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }
  .line2 {
    font-size: 0.88em;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .warn {
    color: var(--warn);
  }
  .err {
    color: var(--danger);
  }
</style>
