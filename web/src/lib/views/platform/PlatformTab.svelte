<script lang="ts">
  // Platform settings (administrators): LLM gateway providers, model catalog, quotas and access levels.
  import { models, errorMessage, type CatalogModel, type LlmProvider, type ModelAlias, type ProviderKind } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { provideActions } from '../../shell/workbench.svelte';
  import ProvidersPane from './ProvidersPane.svelte';
  import CatalogPane from './CatalogPane.svelte';

  let { tab }: { tab: Tab } = $props();

  let section = $state<'providers' | 'catalog'>('providers');
  let kinds = $state<ProviderKind[]>([]);
  let protocols = $state<{ id: string; label?: string }[]>([]);
  let providers = $state<LlmProvider[]>([]);
  let catalog = $state<CatalogModel[]>([]);
  let aliases = $state<ModelAlias[]>([]);
  let loading = $state(true);
  let error = $state('');

  async function load() {
    loading = true;
    try {
      const [k, p, c] = await Promise.all([models.listProviderKinds(), models.listProviders(), models.listCatalog()]);
      kinds = k.kinds ?? [];
      protocols = k.protocols ?? [];
      providers = p.providers ?? [];
      catalog = c.models ?? [];
      aliases = c.aliases ?? [];
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void load();
  });

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', disabled: loading, run: load }],
  );

  const inactive = $derived(providers.filter((p) => p.enabled && !p.active).length);
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="settings" size={18} />
    <h2>Platform settings</h2>
  </div>
  <p class="hint intro">
    LLM gateway: connect providers with their API key, fetch the models they offer, then decide which ones the platform
    exposes, how many tokens they may consume and who may use them. Changes apply immediately.
  </p>

  {#if error}<div class="alert">{error}</div>{/if}

  <div class="seg" role="tablist" aria-label="Platform settings sections">
    <button type="button" role="tab" aria-selected={section === 'providers'} class:on={section === 'providers'} onclick={() => (section = 'providers')}>
      1. Providers <span class="n">{providers.length}</span>
      {#if inactive}<span class="warn" title="Providers that are enabled but not loaded">!</span>{/if}
    </button>
    <button type="button" role="tab" aria-selected={section === 'catalog'} class:on={section === 'catalog'} onclick={() => (section = 'catalog')}>
      2. Models, quotas & access <span class="n">{catalog.length}</span>
    </button>
  </div>

  {#if section === 'providers'}
    <ProvidersPane {providers} {kinds} {protocols} {catalog} onchange={load} openCatalog={() => (section = 'catalog')} />
  {:else}
    <CatalogPane {providers} {catalog} {aliases} onchange={load} openProviders={() => (section = 'providers')} />
  {/if}
</div>

<style>
  .intro {
    max-width: 62rem;
  }
  .seg {
    display: inline-flex;
    gap: 0;
    margin: 0.4rem 0 0.8rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .seg button {
    border: none;
    border-radius: 0;
    background: var(--surface);
    padding: 0.4rem 0.9rem;
    font-weight: 500;
  }
  .seg button.on {
    background: var(--accent);
    color: var(--accent-text);
  }
  .n {
    opacity: 0.75;
    font-weight: 400;
    margin-left: 0.2rem;
  }
  .warn {
    display: inline-block;
    margin-left: 0.3rem;
    min-width: 1.1em;
    border-radius: 50%;
    background: var(--warn);
    color: #000;
    font-size: 0.8em;
    font-weight: 700;
    text-align: center;
  }
</style>
