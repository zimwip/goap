<script lang="ts">
  // Éditeur de code CodeMirror 6 lié à une chaîne (`bind:value`). CodeMirror
  // est chargé à la demande (morceau séparé du bundle).
  import { onMount } from 'svelte';
  import type { CodeLanguage } from '../codemirror';

  type CM = typeof import('../codemirror');

  let {
    value = $bindable(''),
    language = 'text',
    readonly = false,
    dsl = false,
    lineNumbers = true,
    wrap = false,
    placeholder = '',
    minHeight = '6rem',
    maxHeight = '32rem',
    label,
    id,
    bad = false,
    path,
  }: {
    value?: string;
    language?: CodeLanguage;
    readonly?: boolean;
    /** complétion de l'API ctx des actions script */
    dsl?: boolean;
    lineNumbers?: boolean;
    wrap?: boolean;
    placeholder?: string;
    minHeight?: string;
    maxHeight?: string;
    /** libellé accessible */
    label?: string;
    id?: string;
    bad?: boolean;
    /** chemin des problèmes de validation */
    path?: string;
  } = $props();

  let host: HTMLDivElement;
  let cm = $state.raw<CM>();
  let view = $state.raw<InstanceType<CM['EditorView']>>();
  let lang: InstanceType<CM['Compartment']>;
  let ro: InstanceType<CM['Compartment']>;

  function readonlyExt(r: boolean) {
    if (!cm) return [];
    return [cm.EditorState.readOnly.of(r), cm.EditorView.editable.of(!r)];
  }

  onMount(() => {
    let destroyed = false;
    void import('../codemirror').then((mod) => {
      if (destroyed) return;
      cm = mod;
      create(mod);
    });
    return () => {
      destroyed = true;
      view?.destroy();
      view = undefined;
    };
  });

  function create(mod: CM) {
    const { EditorView, EditorState, Compartment, baseExtensions, languageExtension } = mod;
    lang = new Compartment();
    ro = new Compartment();
    view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: value,
        extensions: [
          baseExtensions({ lineNumbers, placeholder, wrap }),
          lang.of(languageExtension(language, dsl)),
          ro.of(readonlyExt(readonly)),
          EditorView.contentAttributes.of({
            ...(label ? { 'aria-label': label } : {}),
            ...(id ? { id } : {}),
          }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) value = u.state.doc.toString();
          }),
        ],
      }),
    });
  }

  // Valeur modifiée de l'extérieur (annulation, rechargement…).
  $effect(() => {
    const v = value;
    if (view && v !== view.state.doc.toString()) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } });
    }
  });

  $effect(() => {
    const l = language;
    const d = dsl;
    if (view && cm) view.dispatch({ effects: lang.reconfigure(cm.languageExtension(l, d)) });
  });

  $effect(() => {
    const r = readonly;
    if (view) view.dispatch({ effects: ro.reconfigure(readonlyExt(r)) });
  });
</script>

<div class="code" class:bad class:readonly data-path={path} style:--min-h={minHeight} style:--max-h={maxHeight}>
  <div bind:this={host}></div>
  {#if !view}
    <!-- Repli pendant le chargement de CodeMirror. -->
    <textarea class="mono fallback" {id} aria-label={label} bind:value readonly={readonly} {placeholder} spellcheck="false"></textarea>
  {/if}
</div>

<style>
  .code {
    min-width: 0;
  }
  .code :global(.cm-editor) {
    min-height: var(--min-h);
    max-height: var(--max-h);
  }
  .code :global(.cm-scroller) {
    min-height: var(--min-h);
  }
  .fallback {
    min-height: var(--min-h);
    resize: none;
  }
  .code.bad :global(.cm-editor) {
    border-color: var(--danger);
    box-shadow: inset 3px 0 0 var(--danger);
  }
</style>
