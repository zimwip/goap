<script lang="ts">
  // "DSL Help" tool: follows the active tab. An editor that holds a script (`EditorView.help`) gets the ctx API
  // of that script's usage, the parameters it declares, a starter script and the sections of docs/dsl.md that
  // describe it; any other tab gets the map of where the DSL is written, then the whole reference
  // (docs/dsl.md, bundled at build time and copied into lib/help).
  import source from '../../help/dsl.md?raw';
  import { renderMarkdown } from '../../markdown';
  import { activeTab, openTab } from '../../shell/tabs.svelte';
  import { editorView } from '../../shell/registry';
  import { notify } from '../../shell/workbench.svelte';
  import {
    fieldNames,
    groupFunctions,
    isRelevant,
    overview,
    paramAccess,
    paramNote,
    signature,
    splitSections,
    starter,
    usageHelp,
  } from '../../dslHelp';

  const sections = splitSections(source).map((s) => ({ ...s, html: renderMarkdown(s.body) }));

  const tab = $derived(activeTab());
  const target = $derived(tab ? editorView(tab.kind)?.help?.(tab) : undefined);
  const usage = $derived(target?.usage);
  const help = $derived(usage ? usageHelp(usage) : undefined);

  let query = $state('');
  const groups = $derived(help ? groupFunctions(help.functions, query) : []);
  const code = $derived(usage ? starter(usage) : '');

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      notify('Copied.', 'info');
    } catch {
      notify('Could not copy: select the text and copy it.', 'error');
    }
  }
</script>

<article class="dsl">
  {#if target}
    <header>
      <h3>{target.title}</h3>
      <p class="hint">{target.subtitle}{help ? ` · ${help.title}` : ''}</p>
    </header>
    {#if target.note}<p class="note">{target.note}</p>{/if}
  {:else}
    <header>
      <h3>DSL</h3>
      <p class="hint">Open a script action, an algorithm or an adapter to see the API of its script here.</p>
    </header>
    <table class="map" aria-label="Where the DSL is written">
      <tbody>
        {#each overview() as o (o.usage)}
          <tr>
            <th scope="row">{o.title}</th>
            <td>{o.where}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}

  {#if target?.links}
    <section>
      <h4>{target.links.heading}</h4>
      <ul class="links">
        {#each target.links.items as l (l.spec.params.uid ?? l.label)}
          <li>
            <button type="button" class="link" onclick={() => openTab(l.spec, { pin: true })}>{l.label}</button>
            {#if l.detail}<span class="hint">{l.detail}</span>{/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if help}
    <section>
      <p>{help.description}</p>
      <p class="hint">{help.contract}</p>
      <p class="hint"><strong>Where:</strong> {help.where}</p>
    </section>

    {#if target?.params}
      <section>
        <h4>Parameters</h4>
        {#if target.params.length}
          <table>
            <tbody>
              {#each target.params as p (p.name)}
                <tr>
                  <td><code>{paramAccess(p)}</code></td>
                  <td>
                    {p.type}
                    {#if paramNote(p)}<span class="hint"> · {paramNote(p)}</span>{/if}
                    {#if p.description}<div class="hint">{p.description}</div>{/if}
                    {#if p.value !== undefined}<div class="hint">value: <code>{JSON.stringify(p.value)}</code></div>{/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}
          <p class="hint">No parameter declared: the script gets none from <code>ctx.param</code>.</p>
        {/if}
      </section>
    {/if}

    {#if target?.plugs}
      <section>
        <h4>Plugged in</h4>
        {#if target.plugs.length}
          <ul>{#each target.plugs as p (p)}<li>{p}</li>{/each}</ul>
        {:else}
          <p class="hint">Not plugged anywhere yet: add it to a node type, an attribute or a transition.</p>
        {/if}
      </section>
    {/if}

    <section>
      <h4>ctx API</h4>
      <input type="search" bind:value={query} placeholder="Filter the {help.functions.length} functions…" aria-label="Filter the ctx functions" />
      {#each groups as g (g.group)}
        <h5>{g.title}</h5>
        <dl class="fns">
          {#each g.fns as f (f.name)}
            <dt><code>{signature(f)}</code>{#if f.returns}<span class="ret"> → {f.returns}</span>{/if}</dt>
            <dd>{f.doc}</dd>
          {/each}
        </dl>
      {:else}
        <p class="empty">No function matches “{query}”.</p>
      {/each}
    </section>

    {#if help.shapes.length}
      <section>
        <h4>Objects</h4>
        <dl class="fns">
          {#each help.shapes as s (s.name)}
            <dt><code>{s.name}</code></dt>
            <dd>{s.summary}<br /><code>{fieldNames(s).join(', ')}</code></dd>
          {/each}
        </dl>
      </section>
    {/if}

    <section>
      <h4>
        Starter script
        <button type="button" class="small" onclick={() => copy(code)}>Copy</button>
      </h4>
      <pre><code>{code}</code></pre>
    </section>
  {/if}

  <section>
    <h4>Reference</h4>
    {#each sections as s (s.title)}
      {#if s.title}
        <details open={isRelevant(s, usage)}>
          <summary>{s.title}</summary>
          <!-- Content bundled at build time (repo file), escaped by renderMarkdown. -->
          <div class="md">{@html s.html}</div>
        </details>
      {/if}
    {/each}
  </section>
</article>

<style>
  .dsl {
    padding: 0 0.8rem 1.5rem;
    font-size: 0.95em;
  }
  h3 {
    margin: 0 0 0.1rem;
    overflow-wrap: anywhere;
  }
  h4 {
    margin: 1rem 0 0.3rem;
    font-size: 0.95rem;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  h5 {
    margin: 0.7rem 0 0.2rem;
    font-size: 0.8rem;
    text-transform: uppercase;
    color: var(--muted);
  }
  .note {
    padding: 0.4rem 0.6rem;
    background: var(--surface-2);
    border-radius: 4px;
  }
  .links {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .links li {
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
  }
  input[type='search'] {
    width: 100%;
    box-sizing: border-box;
    margin-bottom: 0.2rem;
  }
  .fns {
    margin: 0;
  }
  .fns dt {
    margin-top: 0.45rem;
    overflow-wrap: anywhere;
  }
  .fns dd {
    margin: 0 0 0 0.6rem;
    color: var(--muted);
    overflow-wrap: anywhere;
  }
  .ret {
    color: var(--muted);
    font-size: 0.9em;
  }
  table {
    font-size: 0.9em;
    margin-bottom: 0.6rem;
    border-collapse: collapse;
  }
  td,
  th {
    padding: 0.15rem 0.3rem;
    text-align: left;
    vertical-align: top;
  }
  pre {
    font-size: 0.88em;
    white-space: pre;
    overflow-x: auto;
    margin: 0 0 0.6rem;
  }
  code {
    background: var(--surface-2);
    border-radius: 3px;
    padding: 0 2px;
  }
  pre code {
    background: none;
    padding: 0;
  }
  summary {
    cursor: pointer;
    font-weight: 600;
    margin: 0.3rem 0;
  }
  .md :global(h3) {
    font-size: 0.95rem;
    margin-top: 1rem;
  }
  .md :global(table) {
    font-size: 0.9em;
    margin-bottom: 0.6rem;
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
