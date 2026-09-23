<script lang="ts">
  // "Import" tab: import a YAML definition (file or text).
  import { registry, errorMessage, type Issue, type Methodology } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import CodeEditor from '../../components/CodeEditor.svelte';
  import { provideActions, notify } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { refreshMethodologies } from '../../stores/catalog.svelte';
  import { methodologySpec } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  let yaml = $state('');
  let publish = $state(false);
  let importing = $state(false);
  let error = $state('');
  let imported = $state<{ methodology?: Methodology; issues: Issue[] } | null>(null);

  async function readFile(e: Event & { currentTarget: HTMLInputElement }) {
    const file = e.currentTarget.files?.[0];
    if (!file) return;
    try {
      yaml = await file.text();
    } catch (err) {
      error = errorMessage(err);
    }
  }

  async function doImport() {
    if (!yaml.trim()) return;
    importing = true;
    error = '';
    imported = null;
    try {
      const res = await registry.importMethodology(yaml, publish);
      imported = { methodology: res.methodology, issues: res.issues ?? [] };
      void refreshMethodologies();
      const m = res.methodology;
      if (m) notify(`Imported: ${m.name} v${m.version}.`, 'ok');
    } catch (err) {
      error = errorMessage(err);
    } finally {
      importing = false;
    }
  }

  provideActions(
    () => tab.id,
    () => [
      {
        id: 'save',
        label: importing ? 'Import…' : 'Import',
        icon: 'upload',
        primary: true,
        shortcut: 'Ctrl+S',
        disabled: importing || !yaml.trim(),
        run: doImport,
      },
    ],
  );
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="upload" size={18} />
    <h2>Import a methodology (YAML)</h2>
  </div>
  <section class="card">
    <div class="field">
      <label for="imp-file">File</label>
      <input id="imp-file" type="file" accept=".yaml,.yml,text/yaml,application/yaml" onchange={readFile} />
    </div>
    <div class="field">
      <label for="imp-yaml">… or YAML content</label>
      <CodeEditor id="imp-yaml" bind:value={yaml} label="YAML content" minHeight="18rem" maxHeight="55vh" placeholder="name: …" />
    </div>
    <label class="check field">
      <input type="checkbox" bind:checked={publish} />
      Publish directly <span class="opt hint">(the definition must be valid)</span>
    </label>
    {#if error}<div class="alert">{error}</div>{/if}
    {#if imported}
      {@const m = imported.methodology}
      <div class="alert" class:ok={imported.issues.length === 0} class:warn={imported.issues.length > 0}>
        {#if m}
          Imported: <strong>{m.name}</strong> v{m.version} ({m.status === 'published' ? 'published' : 'draft'}).
          <button type="button" class="link" onclick={() => openTab(methodologySpec(m.name ?? '', m.version ?? ''), { pin: true })}>Open →</button>
        {:else}
          Import complete.
        {/if}
      </div>
      {#if imported.issues.length}
        <ul class="issues">
          {#each imported.issues as i, k (k)}
            <li>{#if i.path}<code>{i.path}</code> {/if}{i.message}</li>
          {/each}
        </ul>
      {/if}
    {/if}
    <button class="primary" type="button" onclick={doImport} disabled={importing || !yaml.trim()}>
      {importing ? 'Import…' : 'Import'}
    </button>
  </section>
</div>

<style>
  .issues {
    margin: -0.3rem 0 0.8rem;
    padding-left: 1.2rem;
  }
  .issues code {
    color: var(--danger);
  }
</style>
