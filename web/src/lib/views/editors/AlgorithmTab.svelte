<script lang="ts">
  // "Algorithm" tab: script (JavaScript or Go) of a fixed type with declared parameters,
  // edited in the draft of its domain (ADR 0018).
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import CodeEditor from '../../components/CodeEditor.svelte';
  import AlgorithmTryIt from '../../components/AlgorithmTryIt.svelte';
  import { provideActions, useReveal, notify } from '../../shell/workbench.svelte';
  import { closeWhere, openTab } from '../../shell/tabs.svelte';
  import { moveItem } from '../../methodologyForm';
  import { emptyInstance, emptyParam, freeName, ALGORITHM_LANGUAGES, PARAM_TYPES } from '../../algorithmForm';
  import { ALGORITHM_TEMPLATES, ALGORITHM_USAGES, algorithmUsage, type AlgorithmUsage } from '../../dsl';
  import { algorithmIndex, algorithmToolbar, domainDraftOf, instanceSpec } from './domainTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => domainDraftOf(tab));
  const index = $derived(algorithmIndex(d, tab));
  const a = $derived(index >= 0 ? d.form.algorithms[index] : undefined);
  const usage = $derived(a ? algorithmUsage(a.type) : undefined);
  const instances = $derived(a ? d.form.instances.filter((i) => i.algorithm === a.name && a.name) : []);
  let root = $state<HTMLElement>();

  // keep the tab bound to the algorithm when it is renamed or the draft is reloaded
  $effect(() => {
    if (!a) return;
    tab.params.uid = a.uid;
    tab.params.alg = a.name;
  });

  const bad = (p: string) => d.bad(`algorithms[${index}]${p}`);
  const issues = $derived(d.allIssues.filter((i) => i.norm === `algorithms[${index}]` || i.norm.startsWith(`algorithms[${index}].`) || i.norm.startsWith(`algorithms[${index}][`)));

  /** is the code still the starter template (so that it follows the type / language)? */
  function untouched(): boolean {
    if (!a) return false;
    return !a.code.trim() || Object.values(ALGORITHM_TEMPLATES).some((t) => Object.values(t).includes(a.code));
  }
  function changed() {
    if (!a || !untouched()) return;
    a.code = ALGORITHM_TEMPLATES[a.type as AlgorithmUsage]?.[a.language as 'javascript' | 'go'] ?? a.code;
  }

  // renaming an algorithm renames the references of its instances
  let prev = { uid: '', name: '' };
  $effect(() => {
    const cur = a ? { uid: a.uid, name: a.name } : { uid: '', name: '' };
    const before = untrack(() => prev);
    prev = cur;
    if (!cur.uid || before.uid !== cur.uid || !before.name || !cur.name || before.name === cur.name) return;
    untrack(() => {
      for (const i of d.form.instances) if (i.algorithm === before.name) i.algorithm = cur.name;
    });
  });

  function remove() {
    if (!a) return;
    if (instances.length) {
      notify(`Delete the ${instances.length} instance(s) of ${a.name} first.`, 'error');
      return;
    }
    if (!confirm(`Remove the algorithm "${a.name || 'unnamed'}" from the draft?`)) return;
    const uid = a.uid;
    closeWhere((t) => t.kind === 'algorithm' && t.params.uid === uid);
    d.form.algorithms.splice(index, 1);
  }

  function newInstance() {
    if (!a) return;
    const inst = emptyInstance(a.name, freeName(a.name || 'instance', d.form.instances.map((i) => i.name)));
    d.form.instances.push(inst);
    openTab(instanceSpec(d.name, d.version, inst.uid, inst.name), { pin: true });
  }

  provideActions(
    () => tab.id,
    () => algorithmToolbar(d, { label: 'Delete algorithm', run: remove }),
  );
  useReveal(
    () => tab.id,
    () => root,
  );
</script>

<div class="editor-page" bind:this={root}>
  {#if d.loading}
    <p class="empty">Loading…</p>
  {:else if d.loadError}
    <div class="alert">{d.loadError}</div>
  {:else if !a}
    <div class="alert warn">Algorithm not found in the draft {d.label} (deleted or renamed?).</div>
  {:else}
    <div class="editor-head">
      <Icon name="code" size={18} />
      <h2>{a.name || '(unnamed)'}</h2>
      <span class="hint">algorithm · {d.label}</span>
      <StatusBadge status={d.status} />
      {#if d.dirty}<span class="dirty" title="Unsaved changes">● modified</span>{/if}
    </div>
    {#if d.error}<div class="alert">{d.error}</div>{/if}
    {#if d.readonly}
      <div class="alert info">
        {d.status === 'published' ? 'Published version: it is immutable. Create a new version of the domain to modify it.' : 'Archived version: read-only.'}
      </div>
    {/if}
    {#if issues.length}
      <div class="alert warn" data-testid="algorithm-issues">
        <ul class="plain-list">{#each issues as i}<li><code>{i.norm}</code>: {i.message}</li>{/each}</ul>
      </div>
    {/if}

    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="alg-name">Name</label>
            <input id="alg-name" type="text" class="mono" bind:value={a.name} class:bad={bad('.name')} data-path="algorithms[{index}].name" placeholder="regex-match" />
          </div>
          <div class="field">
            <label for="alg-type">Type</label>
            <select id="alg-type" bind:value={a.type} onchange={changed} class:bad={bad('.type')} data-path="algorithms[{index}].type">
              {#each ALGORITHM_USAGES as u (u.usage)}<option value={u.usage}>{u.title}</option>{/each}
            </select>
          </div>
          <div class="field">
            <label for="alg-lang">Language</label>
            <select id="alg-lang" bind:value={a.language} onchange={changed} class:bad={bad('.language')}>
              {#each ALGORITHM_LANGUAGES as l (l)}<option value={l}>{l}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="alg-desc">Description</label>
          <input id="alg-desc" type="text" bind:value={a.description} />
        </div>
        {#if usage}
          <p class="hint"><strong>{usage.title}.</strong> {usage.description} {usage.contract}</p>
        {/if}
      </section>

      <section class="card">
        <h3>Parameters</h3>
        <p class="hint">Typed values an instance sets and the code reads with <code>ctx.param(name)</code> (Go: <code>ctx.Param(name)</code>). A regex is checked when the instance is saved.</p>
        {#each a.params as p, i}
          <div class="prow" data-path="algorithms[{index}].params[{i}]">
            <input type="text" class="mono" aria-label="Parameter name" bind:value={p.name} placeholder="pattern" />
            <select aria-label="Parameter type" bind:value={p.type}>
              {#each PARAM_TYPES as t (t)}<option value={t}>{t}</option>{/each}
            </select>
            <label class="check"><input type="checkbox" bind:checked={p.required} /> required</label>
            <input type="text" class="mono" aria-label="Default value" bind:value={p.defaultValue} placeholder={p.type === 'strings' ? 'default: a, b' : p.type === 'json' ? 'default: JSON' : 'default'} />
            {#if p.type === 'enum'}<input type="text" class="mono" aria-label="Enum values" bind:value={p.values} placeholder="values: a, b, c" />{/if}
            <input type="text" class="desc" aria-label="Parameter description" bind:value={p.description} placeholder="Description" />
            {#if !d.readonly}
              <RowTools index={i} count={a.params.length} label="the parameter" onmove={(delta) => moveItem(a.params, i, delta)} onremove={() => a.params.splice(i, 1)} />
            {/if}
          </div>
        {:else}
          <p class="empty">No parameters.</p>
        {/each}
        {#if !d.readonly}<button type="button" class="small" onclick={() => a.params.push(emptyParam())}>+ Parameter</button>{/if}
      </section>

      <section class="card">
        <h3>Code</h3>
        {#if usage}
          <p class="hint">
            {#if a.language === 'go'}
              Declare <code>func Run(ctx *dsl.{usage.goCtx}) error</code>; methods are PascalCase (<code>ctx.Value()</code>).
            {:else}
              The code is the body of a function of <code>ctx</code>: it may <code>return</code> (<code>false</code> or a message rejects).
            {/if}
            Pure: no blackboard, LLM or tool calls.
          </p>
          <details class="ctxdoc">
            <summary>ctx API ({usage.functions.length} functions)</summary>
            <ul class="plain-list">
              {#each usage.functions as f (f.name)}
                <li><code>ctx.{a.language === 'go' ? f.name.charAt(0).toUpperCase() + f.name.slice(1) : f.name}({f.args.join(', ')})</code>{f.returns ? ` → ${f.returns}` : ''} <span class="hint">{f.doc}</span></li>
              {/each}
            </ul>
          </details>
        {/if}
        <CodeEditor bind:value={a.code} language={a.language === 'go' ? 'go' : 'javascript'} dsl={a.type} readonly={d.readonly} label="Algorithm code" bad={bad('.code')} path="algorithms[{index}].code" minHeight="14rem" maxHeight="34rem" />
      </section>
    </fieldset>

    <section class="card">
      <h3>Instances</h3>
      {#each instances as inst (inst.uid)}
        <div class="irow">
          <button type="button" class="link mono" onclick={() => openTab(instanceSpec(d.name, d.version, inst.uid, inst.name), { pin: true })}>{inst.name || '(unnamed)'}</button>
          <span class="hint">{Object.entries(inst.values).map(([k, v]) => `${k}=${JSON.stringify(v)}`).join(' · ')}</span>
        </div>
      {:else}
        <p class="empty">No instance: create one to plug this algorithm.</p>
      {/each}
      {#if !d.readonly}<button type="button" class="small" onclick={newInstance}>+ Instance</button>{/if}
    </section>

    <AlgorithmTryIt algorithm={a} />
  {/if}
</div>

<style>
  .prow {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    margin-bottom: 0.3rem;
  }
  .prow input[type='text']:first-child {
    width: 10rem;
  }
  .prow select {
    width: auto;
  }
  .prow .desc {
    flex: 1;
    min-width: 10rem;
  }
  .check {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    font-weight: 400;
  }
  .ctxdoc {
    margin-bottom: 0.4rem;
    font-size: 0.88rem;
  }
  .plain-list {
    list-style: none;
    margin: 0.2rem 0;
    padding: 0;
    display: grid;
    gap: 0.15rem;
  }
  .irow {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
  }
  .dirty {
    color: var(--warn);
    font-size: 0.85rem;
    font-weight: 600;
  }
</style>
