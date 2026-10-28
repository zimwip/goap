<script lang="ts">
  // An address shown as a link (an attribute of widget url, ADR 0171): a click asks the graph how the type of source of
  // its scheme opens it, then opens a node in its editor, a web address in a tab of the browser, and shows a text, an
  // image or where a file lives in a dialog. Logic in `address.ts`.
  import { errorMessage } from '../api';
  import { formatCode } from '../codeText';
  import { openingOfAddress, type Opening } from '../address';
  import { openNode } from '../nodeEditors';
  import Icon from '../shell/Icon.svelte';
  import CodeView from './CodeView.svelte';

  let { address, label = '' }: { address: string; label?: string } = $props();

  let busy = $state(false);
  let error = $state('');
  let shown = $state<Exclude<Opening, { kind: 'node' } | { kind: 'tab' }> | undefined>();
  let copied = $state(false);

  async function open() {
    busy = true;
    error = '';
    try {
      const o = await openingOfAddress(address);
      if (o.kind === 'node') await openNode({ ...o.node, props: {} });
      else if (o.kind === 'tab') window.open(o.href, '_blank', 'noopener');
      else shown = o;
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = true;
      setTimeout(() => (copied = false), 1200);
    } catch {
      /* no clipboard */
    }
  }
</script>

<svelte:window onkeydown={(e) => shown && e.key === 'Escape' && (shown = undefined)} />

<span class="address">
  <button type="button" class="link mono" disabled={busy} title="Open {address}" onclick={() => void open()}>{label || address}</button>
  {#if error}<span class="error" role="alert">{error}</span>{/if}
</span>

{#if shown}
  {@const s = shown}
  <div class="backdrop" role="presentation" onmousedown={() => (shown = undefined)}>
    <div class="panel" role="dialog" aria-modal="true" aria-label={s.title} tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
      <header class="head">
        <h2 class="mono">{s.title}</h2>
        <span class="grow"></span>
        <button type="button" class="ghost small" aria-label="Close" onclick={() => (shown = undefined)}><Icon name="x" size={14} /></button>
      </header>
      <div class="body">
        {#if s.kind === 'text'}
          {#if s.truncated}<p class="hint">Only the beginning of the file is shown.</p>{/if}
          <CodeView value={formatCode({ language: s.language, code: s.content })} label={s.title} maxHeight="60vh" />
        {:else if s.kind === 'image'}
          <img src={s.href} alt={s.title} />
        {:else}
          <p>No browser opens this {s.media ? `${s.media} ` : ''}file: it lives on the machine of the service, at</p>
          <p><code>{s.href}</code> <button type="button" class="small" onclick={() => void copy(s.href)}>{copied ? 'Copied' : 'Copy'}</button></p>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .address {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: baseline;
    min-width: 0;
  }
  .address button.link {
    overflow-wrap: anywhere;
    text-align: left;
  }
  .error {
    color: var(--danger);
    font-size: 0.85em;
  }
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 97;
    display: grid;
    place-items: center;
    background: rgb(0 0 0 / 0.4);
  }
  .panel {
    width: min(960px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    overflow: hidden;
    outline: none;
  }
  .head {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0.7rem 1rem;
    border-bottom: 1px solid var(--border);
    background: var(--chrome-2);
  }
  .head h2 {
    margin: 0;
    font-size: 1.05em;
    overflow-wrap: anywhere;
  }
  .grow {
    flex: 1;
  }
  .body {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0.8rem 1rem;
  }
  img {
    display: block;
    max-width: 100%;
    margin: 0 auto;
  }
</style>
