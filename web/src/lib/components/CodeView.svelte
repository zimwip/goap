<script lang="ts">
  // The value of a code attribute shown as source: its language, the text highlighted with line numbers and the
  // indentation kept (tabs four columns wide), a button to copy the text. A value that does not read as a code text
  // is shown as it is, with the reason.
  import CodeEditor from './CodeEditor.svelte';
  import { editorLanguage, languageLabel, readCode } from '../codeText';

  let { value, maxHeight = '28rem', label = 'Code' }: { value: string; maxHeight?: string; label?: string } = $props();

  const v = $derived(readCode(value));
  const lines = $derived(v.code ? v.code.split('\n').length : 0);
  const meta = $derived(Object.entries(v.meta));
  let copied = $state(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(v.code);
      copied = true;
      setTimeout(() => (copied = false), 1200);
    } catch {
      /* no clipboard */
    }
  }
</script>

<div class="codeview">
  <div class="head">
    {#if v.language}<span class="lang mono" title="Language">{languageLabel(v.language)}</span>{:else}<span class="hint">no language</span>{/if}
    {#each meta as [k, val] (k)}<span class="hint mono" title={k}>{k}: {val}</span>{/each}
    <span class="hint">{lines} line{lines === 1 ? '' : 's'}</span>
    <span class="grow"></span>
    <button type="button" class="small ghost" onclick={copy} disabled={!v.code}>{copied ? 'Copied' : 'Copy'}</button>
  </div>
  {#if v.error}<p class="hint bad">Not a code text ({v.error}): shown as it is.</p>{/if}
  <CodeEditor value={v.code} language={editorLanguage(v.language)} readonly {maxHeight} minHeight="1.5rem" {label} />
</div>

<style>
  .codeview {
    display: grid;
    gap: 0.25rem;
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    font-size: 0.8rem;
  }
  .lang {
    padding: 0 0.4rem;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--text);
  }
  .grow {
    flex: 1;
  }
  .bad {
    color: var(--danger);
  }
  .codeview :global(.cm-content) {
    opacity: 1 !important;
  }
  .codeview :global(.cm-activeLine),
  .codeview :global(.cm-activeLineGutter) {
    background: transparent;
  }
</style>
