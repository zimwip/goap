<script lang="ts">
  // Edition of the value of a code attribute: its language (a known one or any name) and the source text, highlighted
  // as it is typed, Tab indenting. The value bound is the code text (codeText.ts: front matter, then the fenced block);
  // the other keys of its front matter are kept, and a value not edited is left as it came. An empty text with no
  // language leaves the value empty.
  import { untrack } from 'svelte';
  import CodeEditor from './CodeEditor.svelte';
  import { CODE_LANGUAGES, editorLanguage, formatCode, readCode } from '../codeText';

  let { value = $bindable(''), id, label = 'Code' }: { value?: string; id?: string; label?: string } = $props();

  const start = untrack(() => readCode(value));
  let language = $state(start.language);
  let code = $state(start.code);
  const meta = start.error ? {} : start.meta;

  // the value is written again only once edited: a value as it came (a plain text, say) is left as it is
  $effect(() => {
    if (language === start.language && code === start.code) return;
    const next = code === '' && !language.trim() ? '' : formatCode({ language: language.trim(), meta, code });
    if (untrack(() => value) !== next) value = next;
  });
</script>

<div class="codeedit">
  <div class="head">
    <label class="hint" for="{id}-lang">Language</label>
    <input id="{id}-lang" class="mono lang" type="text" list="{id}-langs" bind:value={language} placeholder="go, python…" />
    <datalist id="{id}-langs">
      {#each CODE_LANGUAGES as l (l.value)}<option value={l.value}>{l.label}</option>{/each}
    </datalist>
    {#if language && editorLanguage(language) === 'text'}<span class="hint">not highlighted</span>{/if}
  </div>
  <CodeEditor bind:value={code} language={editorLanguage(language)} {id} {label} minHeight="8rem" maxHeight="32rem" />
</div>

<style>
  .codeedit {
    display: grid;
    gap: 0.3rem;
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .lang {
    width: 12rem;
  }
</style>
