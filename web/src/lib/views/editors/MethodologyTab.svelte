<script lang="ts">
  // Methodology tab: general, target namespace (the node and link types it uses), and
  // content (agents, actions, conditions, goals).
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import EditorPanes, { type Pane } from '../../components/EditorPanes.svelte';
  import DraftHeader from './DraftHeader.svelte';
  import { provideActions, useReveal, notify, revealState } from '../../shell/workbench.svelte';
  import { replaceTab } from '../../shell/tabs.svelte';
  import { drafts, getDraft } from '../../stores/drafts.svelte';
  import { typeCatalog, loadTypes, typeName } from '../../stores/types.svelte';
  import { requestReveal } from '../../shell/workbench.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { openDomain } from './domainTabs';
  import { formatDate, TRIGGER_EVENTS } from '../../api';
  import {
    walkSteps,
    goalChoices,
    type Section,
    type SectionItem,
  } from '../../methodologyForm';
  import {
    draftOf,
    draftActions,
    methodologySpec,
    openItem,
    itemSpec,
    COLLECTION_ICON,
    COLLECTION_LABEL,
    addElement,
    hasSteps,
    sectionsFor,
    type Collection,
  } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  // The component is recreated for each tab: the draft is resolved once.
  const d = untrack(() => draftOf(tab));
  const f = $derived(d.form);
  let root = $state<HTMLElement>();

  async function createNew() {
    const saved = await d.save();
    if (!saved) {
      if (d.error) notify(d.error, 'error');
      return;
    }
    const name = saved.name || d.form.name.trim();
    const version = saved.version || d.form.version.trim();
    drafts.delete('new');
    getDraft(name, version);
    replaceTab(tab.id, methodologySpec(name, version));
    notify(`Methodology ${name} v${version} created.`, 'ok');
  }

  provideActions(
    () => tab.id,
    () =>
      d.isNew
        ? [
            {
              id: 'save',
              label: d.busy === 'save' ? 'Creating…' : 'Create draft',
              icon: 'save',
              primary: true,
              shortcut: 'Ctrl+S',
              disabled: !!d.busy || !d.form.name.trim() || !d.form.version.trim(),
              run: createNew,
            },
          ]
        : draftActions(d),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  $effect(() => {
    void loadTypes();
  });

  const cat = $derived(typeCatalog.cat);
  const ns = $derived(f.namespace.trim());
  const goals = $derived(goalChoices(f));
  const namespaces = $derived(cat.namespaces());

  // The panes come from the domain: the element types the methodology version is composed of (its `defines`
  // composition link), each bound to the collection of the draft that holds its elements. The catalogue not
  // loaded yet, the default list stands in.
  const SECTIONS = $derived(sectionsFor(cat));
  const label = COLLECTION_LABEL;
  const icon = COLLECTION_ICON;

  let pane = $state(untrack(() => tab.params.pane) || 'overview');
  $effect(() => {
    tab.params.pane = pane;
  });

  const panes = $derived<Pane[]>([
    { id: 'overview', label: 'Overview' },
    { id: 'types', label: 'Types', badge: ns || '—' },
    ...SECTIONS.map((s) => ({ id: s, label: label(s), badge: f[s].length })),
  ]);

  // a reveal request (problems console, issue path…) opens the pane that holds the field
  $effect(() => {
    void revealState.seq;
    if (revealState.tabId !== tab.id || !revealState.path) return;
    const path = revealState.path;
    const section = SECTIONS.find((s) => path.startsWith(s));
    pane = section ?? (path.startsWith('namespace') ? 'types' : 'overview');
  });

  /** The parts of an element, as the domain composes them: the steps a process or a method is made of. */
  function parts(s: Collection, it: SectionItem) {
    return hasSteps(cat, s, it) ? walkSteps(it.steps) : [];
  }

  function openPart(s: Section, it: SectionItem, at: string) {
    const t = openTab(itemSpec(d, s, it));
    requestReveal(t.id, at);
  }

  function summary(section: Section, it: SectionItem): string {
    if ('kind' in it) return it.kind;
    if ('expr' in it) return it.expr;
    if ('for' in it) return `${it.for}${it.actions.length ? ` · ${it.actions.length} actions` : ''}${it.when ? ` · when ${it.when}` : ''}`;
    if ('planner' in it) return it.planner;
    if ('steps' in it) return `${walkSteps(it.steps).length} step(s)${it.description ? ` — ${it.description}` : ''}`;
    return it.description;
  }
</script>

<div class="editor-page" bind:this={root}>
  {#if d.loading}
    <p class="empty">Loading…</p>
  {:else if d.loadError}
    <div class="alert">{d.loadError}</div>
  {:else}
    <DraftHeader draft={d} icon="book" kind="Methodology" title={d.isNew ? 'New methodology' : d.label} dirty={d.dirty} />

    {#if d.isNew}
      <fieldset class="plain" disabled={d.readonly}>
      <section class="card" id="m-general">
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="m-name">Name</label>
            <input
              id="m-name"
              type="text"
              class="mono"
              bind:value={f.name}
              disabled={!d.isNew}
              class:bad={d.bad('name')}
              data-path="name"
              placeholder="impact-analysis"
            />
          </div>
          <div class="field">
            <label for="m-version">Version</label>
            <input
              id="m-version"
              type="text"
              class="mono"
              bind:value={f.version}
              disabled={!d.isNew}
              class:bad={d.bad('version')}
              data-path="version"
              placeholder="0.1.0"
            />
          </div>
        </div>
        <div class="field">
          <label for="m-desc">Description</label>
          <textarea id="m-desc" rows="3" bind:value={f.description} class:bad={d.bad('description')} data-path="description"
          ></textarea>
        </div>
        <div class="field">
          <label for="m-imports">Condition libraries <span class="opt">(imported, comma separated: decisions, risks)</span></label>
          <input id="m-imports" type="text" class="mono" bind:value={f.imports} disabled={d.readonly} class:bad={d.bad('imports')} data-path="imports" placeholder="risks" />
        </div>
        <div class="field">
          <label for="m-goal">Main goal <span class="opt">(the goal its changes start with; empty: the first goal or process)</span></label>
          <select id="m-goal" bind:value={f.goal} disabled={d.readonly} class:bad={d.bad('goal')} data-path="goal">
            <option value="">— first goal or process —</option>
            {#if f.goal.trim() && !goals.includes(f.goal.trim())}<option value={f.goal}>{f.goal} (unknown)</option>{/if}
            {#each goals as n (n)}<option value={n}>{n}</option>{/each}
          </select>
          {#each d.warnings.filter((w) => w.norm === 'goal') as w, k (k)}
            <p class="field-warn" role="status">{w.message}</p>
          {/each}
        </div>
        {#if d.meta.updatedAt || d.meta.publishedAt}
          <p class="hint">
            {#if d.meta.createdAt}Created on {formatDate(d.meta.createdAt)}.{/if}
            {#if d.meta.updatedAt}Modified on {formatDate(d.meta.updatedAt)}{d.meta.updatedBy ? ` by ${d.meta.updatedBy}` : ''}.{/if}
            {#if d.meta.publishedAt}Published on {formatDate(d.meta.publishedAt)}.{/if}
          </p>
        {/if}
        {#if d.isNew}
          <p class="hint">Create the draft to add the agents, actions, conditions, and goals.</p>
        {/if}
      </section>
      </fieldset>
    {:else}
      <EditorPanes {panes} bind:active={pane} label="Methodology sections">
        {#snippet children(active)}
          <fieldset class="plain" disabled={d.readonly}>
            {#if active === 'overview'}
      <section class="card" id="m-general">
        <h3>General</h3>
        <div class="grid">
          <div class="field">
            <label for="m-name">Name</label>
            <input
              id="m-name"
              type="text"
              class="mono"
              bind:value={f.name}
              disabled={!d.isNew}
              class:bad={d.bad('name')}
              data-path="name"
              placeholder="impact-analysis"
            />
          </div>
          <div class="field">
            <label for="m-version">Version</label>
            <input
              id="m-version"
              type="text"
              class="mono"
              bind:value={f.version}
              disabled={!d.isNew}
              class:bad={d.bad('version')}
              data-path="version"
              placeholder="0.1.0"
            />
          </div>
        </div>
        <div class="field">
          <label for="m-desc">Description</label>
          <textarea id="m-desc" rows="3" bind:value={f.description} class:bad={d.bad('description')} data-path="description"
          ></textarea>
        </div>
        {#if d.meta.updatedAt || d.meta.publishedAt}
          <p class="hint">
            {#if d.meta.createdAt}Created on {formatDate(d.meta.createdAt)}.{/if}
            {#if d.meta.updatedAt}Modified on {formatDate(d.meta.updatedAt)}{d.meta.updatedBy ? ` by ${d.meta.updatedBy}` : ''}.{/if}
            {#if d.meta.publishedAt}Published on {formatDate(d.meta.publishedAt)}.{/if}
          </p>
        {/if}
        {#if d.isNew}
          <p class="hint">Create the draft to add the agents, actions, conditions, and goals.</p>
        {/if}
      </section>
      {#if !d.isNew}
        <section class="card" data-path="appliesTo">
          <h3>Transverse</h3>
          <p class="hint">
            A transverse methodology runs its processes alongside the changes of the methodologies it applies to, on the same
            change, coupled to them by events only: each event it reacts to runs its processes again, with the event in
            <code>vars.event</code>. The others may wait for its outcome through conditions (e.g. <code>risks_under_control</code>).
          </p>
          <fieldset class="plain" disabled={d.readonly}>
            <div class="field">
              <label for="m-applies">Applies to <span class="opt">(methodologies, comma separated; empty: not transverse)</span></label>
              <input id="m-applies" type="text" class="mono" bind:value={f.appliesTo} class:bad={d.bad('appliesTo')} data-path="appliesTo" placeholder="sdlc" />
            </div>
            {#if f.appliesTo.trim()}
              <div class="label">Reacts to <span class="opt">(empty: a run attached, a step completed)</span></div>
              {#each f.on as sub, i (i)}
                <div class="role-row" class:bad={d.bad(`on[${i}]`)}>
                  <select bind:value={sub.event} aria-label="Event" data-path="on[{i}].event">
                    {#each TRIGGER_EVENTS as ev (ev)}<option value={ev}>{ev}</option>{/each}
                  </select>
                  <input type="text" class="mono" aria-label="Filter" placeholder={'CEL over event, e.g. event.step.process == "software_delivery"'} bind:value={sub.filter} data-path="on[{i}].filter" />
                  {#if !d.readonly}<button type="button" class="small icon" aria-label="Remove the event" onclick={() => f.on.splice(i, 1)}>✕</button>{/if}
                </div>
              {/each}
              {#if !d.readonly}<button type="button" class="small" onclick={() => f.on.push({ event: 'step.completed', filter: '' })}>+ Event</button>{/if}
            {/if}
          </fieldset>
        </section>
      {/if}
            {:else if active === 'types'}
        <section class="card" id="m-types">
          <h3>Types</h3>
          <div class="field">
            <label for="m-namespace">Target namespace</label>
            <select id="m-namespace" bind:value={f.namespace} disabled={d.readonly} data-path="namespace" class:bad={d.bad('namespace')}>
              <option value="">— choose a namespace —</option>
              {#if ns && !namespaces.includes(ns)}<option value={ns}>{ns} (unknown)</option>{/if}
              {#each namespaces as n (n)}<option value={n}>{n}</option>{/each}
            </select>
          </div>
          <p class="hint">
            The changes of the methodology act on this namespace; its node types are referenced as
            <code>{ns || '<namespace>'}@&lt;NodeType&gt;</code>. They are defined by the domain of the namespace (read-only here){#if ns && cat.domains[ns]}:
              <button type="button" class="link" onclick={() => openDomain(ns, cat.domains[ns])}>open the domain {ns} v{cat.domains[ns]}</button> to edit them{/if}.
          </p>
          {#if typeCatalog.error}<div class="alert">{typeCatalog.error}</div>{/if}
          {#if ns}
            <h4>Node types</h4>
            <ul class="plain-list">
              {#each d.nodeTypeNames as t (t)}
                {@const info = cat.type(t)}
                <li>
                  <code>{typeName(t)}</code>{#if info?.ancestors?.length}<span class="hint">{` extends ${info.ancestors.map(typeName).join(' › ')}`}</span>{/if}{#if info?.attributes?.length}<span class="hint">{` · ${info.attributes.map((a) => a.attribute?.name).join(', ')}`}</span>{/if}
                </li>
              {:else}
                <li class="empty">{typeCatalog.loaded ? 'No node types in this namespace.' : 'Loading…'}</li>
              {/each}
            </ul>
            <h4 class="sub">Link types</h4>
            <ul class="plain-list">
              {#each cat.links.filter((l) => d.linkTypeNames.includes(l.ref ?? '')) as l (l.ref)}
                <li><code>{typeName(l.ref)}</code>{#if l.from || l.to}<span class="hint">{` ${l.from || 'any'} → ${l.to || 'any'}`}</span>{/if}</li>
              {:else}
                <li class="empty">None.</li>
              {/each}
            </ul>
          {/if}
        </section>
            {:else if active === 'roles' && SECTIONS.includes('roles')}
        <section class="card" data-path="roles">
                <div class="row head">
                  <h3 class="grow">Roles <span class="hint">{f.roles.length}</span></h3>
                  {#if !d.readonly}<button type="button" class="small" onclick={() => f.roles.push({ name: '', description: '' })}>+ Role</button>{/if}
                </div>
                <p class="hint">
                  The roles the processes and methods assign (responsible, accountable, consulted, informed). The methodology
                  names roles, never people: the organisation assigns them to users per unit (<code>developer@TEAM-PAY</code>), and a
                  role held in a unit holds in the units below it.
                </p>
                <fieldset class="plain" disabled={d.readonly}>
                  {#each f.roles as r, i (i)}
                    <div class="role-row" class:bad={d.bad(`roles[${i}]`)}>
                      <input type="text" class="mono" aria-label="Role name" placeholder="developer" bind:value={r.name} data-path="roles[{i}].name" />
                      <input type="text" aria-label="Role description" placeholder="What the role does" bind:value={r.description} />
                      {#if !d.readonly}<button type="button" class="small icon" aria-label="Remove the role" onclick={() => f.roles.splice(i, 1)}>✕</button>{/if}
                    </div>
                  {:else}
                    <p class="empty">No role declared.</p>
                  {/each}
                </fieldset>
              </section>
            {:else if SECTIONS.includes(active as Collection) && active !== 'roles'}
              {@const s = active as Section}
              <section class="card" data-path={s}>
                <div class="row head">
                  <Icon name={icon(s)} size={16} />
                  <h3 class="grow">{label(s)} <span class="hint">{f[s].length}</span></h3>
                  {#if !d.readonly}
                    <button type="button" class="small primary" onclick={() => addElement(d, s)}>+ Add</button>
                  {/if}
                </div>
                <p class="hint">Click an element to open it; double-click to keep its tab open.</p>
                <ul class="items">
                  {#each f[s] as it, i (it.uid)}
                    {@const n = d.count(`${s}[${i}]`)}
                    <li data-path="{s}[{i}]" class:has-issues={n > 0}>
                      <button type="button" class="link" onclick={() => openItem(d, s, it)} ondblclick={() => openItem(d, s, it, true)}>{it.name || '(unnamed)'}</button>
                      <span class="hint ell">{summary(s, it)}</span>
                      {#if n}<span class="count bad" title="Issues">{n}</span>{/if}
                    </li>
                    {#each parts(s, it) as p (p.path)}
                      <li class="part" style:padding-left="{1.2 + 1.1 * (p.path.split('/').length - 1)}rem" data-path="{s}[{i}].{p.at}">
                        <button type="button" class="link" onclick={() => openPart(s, it, p.at)}>{p.step.name || '(unnamed)'}</button>
                        <span class="hint ell">{p.step.description}</span>
                      </li>
                    {/each}
                  {:else}
                    <li class="empty">{s === 'agents' ? 'No agent: the default agent runs every action.' : s === 'processes' ? 'No process: describe the steps that reach the objective of a change, each made of sub-steps, actions, variants (methods), a nested process or a person.' : 'None yet.'}</li>
                  {/each}
                </ul>
              </section>
            {/if}
          </fieldset>
        {/snippet}
      </EditorPanes>
    {/if}
  {/if}
</div>

<style>
  .field-warn {
    margin: 0.25rem 0 0;
    font-size: 0.85em;
    color: var(--warn);
  }
  .row.head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .items {
    list-style: none;
    margin: 0.4rem 0 0;
    padding: 0;
    display: grid;
    gap: 0.2rem;
  }
  .items li {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    padding: 0.2rem 0.4rem;
    border-radius: 4px;
  }
  .items li:hover {
    background: var(--hover);
  }
  .items li.has-issues {
    box-shadow: inset 3px 0 0 var(--danger);
  }
  .part {
    font-size: 0.92em;
  }
  .count.bad {
    color: var(--danger);
  }
  h4.sub {
    margin-top: 1rem;
  }
  .plain-list {
    list-style: none;
    margin: 0 0 0.4rem;
    padding: 0;
    display: grid;
    gap: 0.15rem;
  }
  .ell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1;
    font-family: var(--mono);
    font-size: 0.85em;
  }
  .count {
    color: var(--danger);
    font-weight: 700;
    font-size: 0.8rem;
  }
  .role-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 3fr) auto;
    gap: 6px;
    margin-bottom: 4px;
  }
</style>
