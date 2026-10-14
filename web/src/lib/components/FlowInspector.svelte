<script lang="ts">
  // The panel of the flow's introspection (flowInspect.ts): what the context menu asked to see about a condition, a
  // step, a boundary block or the level. A step or a condition named in it opens its own inspection; Back returns.
  import Icon from '../shell/Icon.svelte';
  import { inspectionText, type Inspection } from '../flowInspect';

  let {
    inspection,
    canBack = false,
    onback,
    onclose,
    onstep,
    oncondition,
  }: {
    inspection: Inspection;
    canBack?: boolean;
    onback?: () => void;
    onclose: () => void;
    onstep?: (path: string) => void;
    oncondition?: (name: string) => void;
  } = $props();

  let copied = $state('');
  async function copy(text: string, what: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = what;
      setTimeout(() => (copied === what ? (copied = '') : undefined), 1200);
    } catch {
      copied = '';
    }
  }
</script>

<aside class="inspector" aria-label="Inspection of {inspection.title}">
  <header>
    {#if canBack}<button type="button" class="ic" onclick={onback} title="Back" aria-label="Back"><Icon name="chevronLeft" size={14} /></button>{/if}
    <div class="ttl">
      <b class="mono">{inspection.title}</b>
      {#if inspection.subtitle}<span class="sub">{inspection.subtitle}</span>{/if}
    </div>
    <button type="button" class="ic" onclick={() => copy(inspectionText(inspection), 'all')} title={copied === 'all' ? 'Copied' : 'Copy as text'} aria-label="Copy as text"><Icon name={copied === 'all' ? 'check' : 'copy'} size={14} /></button>
    <button type="button" class="ic" onclick={onclose} title="Close (Esc)" aria-label="Close"><Icon name="x" size={14} /></button>
  </header>
  <div class="body">
    {#each inspection.sections as s, si (si)}
      <section>
        <h4>{s.title}</h4>
        {#if s.text}<p class="text">{s.text}</p>{/if}
        {#if s.code}
          <div class="code">
            <pre>{s.code}</pre>
            <button type="button" class="ic cp" onclick={() => copy(s.code ?? '', `s${si}`)} title={copied === `s${si}` ? 'Copied' : 'Copy'} aria-label="Copy"><Icon name={copied === `s${si}` ? 'check' : 'copy'} size={12} /></button>
          </div>
        {/if}
        {#if s.rows?.length}
          <ul>
            {#each s.rows as r, ri (ri)}
              <li class={r.tone ?? ''}>
                <div class="row">
                  {#if r.step}<button type="button" class="link mono" onclick={() => onstep?.(r.step ?? '')} title="Inspect this step">{r.label}</button>
                  {:else if r.condition}<button type="button" class="link mono" onclick={() => oncondition?.(r.condition ?? '')} title="Inspect this condition">{r.label}</button>
                  {:else}<span class="lbl">{r.label}</span>{/if}
                  {#if r.value}<span class="val">{r.value}</span>{/if}
                </div>
                {#if r.code}<pre class="inline">{r.code}</pre>{:else if r.condition && r.tone === 'warn'}<span class="val">no expression: set by a step's exit</span>{/if}
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/each}
  </div>
</aside>

<style>
  .inspector {
    position: absolute;
    top: 48px;
    right: 10px;
    bottom: 10px;
    z-index: 8;
    width: min(380px, calc(100% - 20px));
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
    font-size: 12px;
  }
  header {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  .ttl {
    flex: 1;
    min-width: 0;
    display: flex;
    gap: 6px;
    align-items: baseline;
    overflow: hidden;
  }
  .ttl b {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    color: var(--muted);
    white-space: nowrap;
  }
  .ic {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    min-height: 0;
    padding: 0;
    border: 0;
    background: none;
    color: var(--text);
    border-radius: 4px;
    cursor: pointer;
  }
  .ic:hover {
    background: var(--hover);
  }
  .body {
    overflow: auto;
    padding: 4px 10px 10px;
  }
  h4 {
    margin: 10px 0 4px;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--muted);
  }
  .text {
    margin: 0 0 4px;
    color: var(--muted);
    white-space: pre-wrap;
  }
  .code {
    position: relative;
  }
  .code .cp {
    position: absolute;
    top: 2px;
    right: 2px;
  }
  pre {
    margin: 0;
    padding: 6px 28px 6px 8px;
    background: var(--surface-3);
    border-radius: 4px;
    font-family: var(--mono);
    font-size: 11px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  pre.inline {
    margin-top: 2px;
    padding: 3px 6px;
  }
  ul {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 4px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: baseline;
  }
  .lbl {
    font-weight: 600;
  }
  .val {
    color: var(--muted);
  }
  .link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--accent);
    cursor: pointer;
    min-height: 0;
    text-align: left;
    word-break: break-all;
  }
  .link:hover {
    text-decoration: underline;
  }
  li.warn .lbl,
  li.warn .val {
    color: var(--warn);
  }
  li.bad .lbl,
  li.bad .val {
    color: var(--danger);
  }
  li.ok .val {
    color: var(--ok);
  }
  .mono {
    font-family: var(--mono);
  }
</style>
