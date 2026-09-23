<script lang="ts">
  // Outil « Aide DSL » : docs/dsl.md embarqué au build (copie dans lib/help).
  import source from '../../help/dsl.md?raw';
  import { renderMarkdown } from '../../markdown';

  const html = renderMarkdown(source);
</script>

<!-- Contenu embarqué au build (fichier du dépôt), échappé par renderMarkdown. -->
<article class="md">{@html html}</article>

<style>
  .md {
    padding: 0 0.8rem 1.5rem;
    font-size: 0.95em;
  }
  .md :global(h2) {
    font-size: 1.05rem;
    margin-top: 0.2rem;
  }
  .md :global(h3) {
    font-size: 0.95rem;
    margin-top: 1rem;
  }
  .md :global(table) {
    font-size: 0.9em;
    margin-bottom: 0.6rem;
  }
  .md :global(td),
  .md :global(th) {
    padding: 0.15rem 0.3rem;
  }
  .md :global(pre) {
    font-size: 0.88em;
    white-space: pre;
    margin-bottom: 0.6rem;
  }
  .md :global(code) {
    background: var(--surface-2);
    border-radius: 3px;
    padding: 0 2px;
  }
  .md :global(pre code) {
    background: none;
    padding: 0;
  }
</style>
