<script lang="ts">
  // Platform settings: one modal (Zed-style) with its own section nav, mounted
  // once at the shell root. Sections are ABAC-gated: "admin" ones only show for
  // a principal holding the admin role; everyone gets their own preferences.
  import { settingsState, closeSettings } from './settingsState.svelte';
  import { hasAnyRole } from '../stores/session.svelte';
  import { confirmState } from './confirmState.svelte';
  import { pending, pendingCount } from '../stores/pending.svelte';
  import { confirmLeaveSettings, saveSettings, discardSettings } from '../stores/settings.svelte';
  import Icon, { type IconName } from './Icon.svelte';
  import PreferencesPane from '../views/platform/PreferencesPane.svelte';
  import ProvidersPane from '../views/platform/ProvidersPane.svelte';
  import CatalogPane from '../views/platform/CatalogPane.svelte';
  import PlatformAlgorithmsPane from '../views/platform/PlatformAlgorithmsPane.svelte';
  import { models, errorMessage, type CatalogModel, type LlmProvider, type ModelAlias, type ProviderKind } from '../api';
  import { overlayAliases, overlayCatalog, overlayProviders, listAliasProposals, type AliasProposal } from '../llmEdit';

  interface Section {
    id: string;
    label: string;
    icon: IconName;
    admin?: boolean;
  }

  const SECTIONS: Section[] = [
    { id: 'preferences', label: 'Preferences', icon: 'user' },
    { id: 'providers', label: 'LLM providers', icon: 'zap', admin: true },
    { id: 'catalog', label: 'Models & quotas', icon: 'database', admin: true },
    { id: 'algorithms', label: 'Algorithms', icon: 'code' },
  ];

  const visible = $derived(SECTIONS.filter((s) => !s.admin || hasAnyRole('admin')));
  const active = $derived(visible.find((s) => s.id === settingsState.section) ?? visible[0]);

  // Fall back to a section the principal can actually see (e.g. admin role revoked mid-session).
  $effect(() => {
    if (settingsState.open && active && active.id !== settingsState.section) settingsState.section = active.id;
  });

  // --- LLM gateway data, shared by the "providers" and "catalog" sections --------

  let kinds = $state<ProviderKind[]>([]);
  let protocols = $state<{ id: string; label?: string }[]>([]);
  // what the gateway runs (applied), and what the dialog shows: that, with the staged edits laid over it
  let applied = $state<{ providers: LlmProvider[]; catalog: CatalogModel[]; aliases: ModelAlias[] }>({ providers: [], catalog: [], aliases: [] });
  let proposals = $state<AliasProposal[]>([]);
  const providers = $derived(overlayProviders(applied.providers));
  const catalog = $derived(overlayCatalog(applied.catalog));
  const aliases = $derived(overlayAliases(applied.aliases));
  const count = $derived(pendingCount());
  let gatewayLoaded = $state(false);
  let gatewayError = $state('');

  async function loadGateway() {
    try {
      const [k, p, c] = await Promise.all([models.listProviderKinds(), models.listProviders(), models.listCatalog()]);
      kinds = k.kinds ?? [];
      protocols = k.protocols ?? [];
      applied = { providers: p.providers ?? [], catalog: c.models ?? [], aliases: c.aliases ?? [] };
      proposals = await listAliasProposals().catch(() => []);
      gatewayError = '';
    } catch (e) {
      gatewayError = errorMessage(e);
    } finally {
      gatewayLoaded = true;
    }
  }

  $effect(() => {
    if (settingsState.open && (active?.id === 'providers' || active?.id === 'catalog') && !gatewayLoaded) void loadGateway();
  });

  // leaving with unsaved preferences asks first: accepting loses them, declining stays in the dialog
  async function requestClose() {
    if (await confirmLeaveSettings(count)) closeSettings();
  }

  // the gateway follows the graph through events: read it again shortly after a change landed
  let later: ReturnType<typeof setTimeout> | undefined;
  async function refreshGateway() {
    await loadGateway();
    clearTimeout(later);
    later = setTimeout(() => void loadGateway(), 1200);
  }

  // Save applies every staged edit, Discard removes them; the gateway is read again since it may have changed
  async function save() {
    if (await saveSettings()) await refreshGateway();
  }

  async function discard() {
    if (await discardSettings()) await refreshGateway();
  }

  let dialog = $state<HTMLDivElement>();

  $effect(() => {
    if (settingsState.open) queueMicrotask(() => dialog?.focus());
  });
</script>

<svelte:window onkeydown={(e) => settingsState.open && e.key === 'Escape' && !confirmState.current && requestClose()} />

{#if settingsState.open && active}
  <div class="backdrop" role="presentation" onmousedown={requestClose}>
    <div
      class="panel"
      role="dialog"
      aria-modal="true"
      aria-label="Settings"
      tabindex="-1"
      bind:this={dialog}
      onmousedown={(e) => e.stopPropagation()}
    >
      <nav class="nav" aria-label="Settings sections">
        <div class="nav-head">Settings</div>
        {#each visible as s (s.id)}
          <button type="button" class="nav-item" class:on={s.id === active.id} onclick={() => (settingsState.section = s.id)}>
            <Icon name={s.icon} size={14} />
            {s.label}
          </button>
        {/each}
      </nav>
      <div class="body">
        <div class="body-head">
          <h2>{active.label}</h2>
          <button type="button" class="ghost small" aria-label="Close settings" onclick={requestClose}><Icon name="x" size={14} /></button>
        </div>
        <div class="body-scroll">
          {#if active.id === 'preferences'}
            <PreferencesPane />
          {:else if active.id === 'providers'}
            {#if gatewayError}<div class="alert">{gatewayError}</div>{/if}
            <ProvidersPane {providers} {kinds} {protocols} {catalog} onchange={refreshGateway} openCatalog={() => (settingsState.section = 'catalog')} />
          {:else if active.id === 'catalog'}
            {#if gatewayError}<div class="alert">{gatewayError}</div>{/if}
            <CatalogPane {providers} {catalog} {aliases} {proposals} onchange={refreshGateway} openProviders={() => (settingsState.section = 'providers')} />
          {:else if active.id === 'algorithms'}
            <PlatformAlgorithmsPane />
          {/if}
        </div>
        <div class="savebar" class:dirty={count > 0}>
          <span class="hint">
            {#if pending.error}<span class="err">{pending.error}</span>
            {:else if count > 0}{count} unsaved change{count > 1 ? 's' : ''}: kept in a personal change that only you see, until you save.
            {:else}Nothing to save.{/if}
          </span>
          <span class="grow"></span>
          <button type="button" class="small" disabled={count === 0 || pending.busy} onclick={discard}>Discard</button>
          <button type="button" class="small primary" disabled={count === 0 || pending.busy} onclick={save}>Save</button>
        </div>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 97;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.4);
  }
  .panel {
    width: min(980px, calc(100vw - 32px));
    height: min(680px, calc(100vh - 48px));
    min-width: 560px;
    min-height: 380px;
    max-width: calc(100vw - 32px);
    max-height: calc(100vh - 48px);
    display: flex;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    resize: both;
  }
  .nav {
    width: 220px;
    flex: none;
    display: flex;
    flex-direction: column;
    background: var(--chrome-2);
    border-right: 1px solid var(--border);
    padding: 0.5rem;
    overflow-y: auto;
  }
  .nav-head {
    font-weight: 700;
    font-size: 0.8em;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
    padding: 0.4rem 0.5rem;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0.5rem;
    border: none;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text);
    font-weight: 500;
    text-align: left;
  }
  .nav-item:hover {
    background: var(--hover);
  }
  .nav-item.on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .body {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .body-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
  }
  .body-head h2 {
    margin: 0;
    font-size: 1.05em;
  }
  .savebar {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    border-top: 1px solid var(--border);
    background: var(--chrome-2);
  }
  .savebar.dirty {
    background: var(--accent-soft);
  }
  .savebar .grow {
    flex: 1;
  }
  .savebar .err {
    color: var(--danger);
  }
  .body-scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 1rem;
  }
</style>
