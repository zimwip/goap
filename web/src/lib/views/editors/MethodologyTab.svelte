<script lang="ts">
  // Onglet « méthodologie » : général, domaine (types de nœuds et de liens) et
  // contenu (agents, actions, conditions, objectifs).
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import RowTools from '../../components/RowTools.svelte';
  import DraftHeader from './DraftHeader.svelte';
  import { provideActions, useReveal, notify } from '../../shell/workbench.svelte';
  import { replaceTab } from '../../shell/tabs.svelte';
  import { drafts, getDraft } from '../../stores/drafts.svelte';
  import { formatDate } from '../../api';
  import {
    emptyNodeType,
    emptyLinkType,
    emptyAgent,
    emptyAction,
    emptyCondition,
    emptyGoal,
    moveItem,
    type Section,
    type SectionItem,
  } from '../../methodologyForm';
  import {
    draftOf,
    draftActions,
    methodologySpec,
    openItem,
    SECTION_ICON,
    SECTION_LABEL,
  } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  // Le composant est recréé pour chaque onglet : le brouillon est résolu une fois.
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
    notify(`Méthodologie ${name} v${version} créée.`, 'ok');
  }

  provideActions(
    () => tab.id,
    () =>
      d.isNew
        ? [
            {
              id: 'save',
              label: d.busy === 'save' ? 'Création…' : 'Créer le brouillon',
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

  const SECTIONS: Section[] = ['agents', 'actions', 'conditions', 'goals'];

  function add(section: Section) {
    const factories = { agents: emptyAgent, actions: emptyAction, conditions: emptyCondition, goals: emptyGoal };
    (d.form[section] as SectionItem[]).push(factories[section]());
    const list = d.form[section];
    openItem(d, section, list[list.length - 1], true);
  }

  function summary(section: Section, it: SectionItem): string {
    if ('kind' in it) return it.kind;
    if ('planner' in it) return it.planner;
    if ('expr' in it) return it.expr;
    return it.description;
  }
</script>

<div class="editor-page" bind:this={root}>
  {#if d.loading}
    <p class="empty">Chargement…</p>
  {:else if d.loadError}
    <div class="alert">{d.loadError}</div>
  {:else}
    <DraftHeader draft={d} icon="book" kind="Méthodologie" title={d.isNew ? 'Nouvelle méthodologie' : d.label} dirty={d.dirty} />

    <fieldset class="plain" disabled={d.readonly}>
      <section class="card" id="m-general">
        <h3>Général</h3>
        <div class="grid">
          <div class="field">
            <label for="m-name">Nom</label>
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
            {#if d.meta.createdAt}Créée le {formatDate(d.meta.createdAt)}.{/if}
            {#if d.meta.updatedAt}Modifiée le {formatDate(d.meta.updatedAt)}{d.meta.updatedBy ? ` par ${d.meta.updatedBy}` : ''}.{/if}
            {#if d.meta.publishedAt}Publiée le {formatDate(d.meta.publishedAt)}.{/if}
          </p>
        {/if}
        {#if d.isNew}
          <p class="hint">Créez le brouillon pour ajouter le domaine, les agents, actions, conditions et objectifs.</p>
        {/if}
      </section>

      {#if !d.isNew}
        <section class="card" id="m-domain">
          <h3>Domaine</h3>
          <h4 data-path="nodeTypes">Types de nœuds</h4>
          {#each f.nodeTypes as n, i}
            <div class="item" class:has-issues={d.count(`nodeTypes[${i}]`) > 0} data-path="nodeTypes[{i}]">
              <input
                type="text"
                class="mono"
                aria-label="Nom du type de nœud"
                bind:value={n.name}
                class:bad={d.bad(`nodeTypes[${i}].name`)}
                data-path="nodeTypes[{i}].name"
                placeholder="Requirement"
              />
              <input
                type="text"
                aria-label="Description"
                bind:value={n.description}
                class:bad={d.bad(`nodeTypes[${i}].description`)}
                data-path="nodeTypes[{i}].description"
                placeholder="Description"
              />
              <input
                type="text"
                class="mono"
                aria-label="Propriétés (séparées par des virgules)"
                bind:value={n.properties}
                class:bad={d.bad(`nodeTypes[${i}].properties`)}
                data-path="nodeTypes[{i}].properties"
                placeholder="title, description"
              />
              {#if !d.readonly}
                <RowTools
                  index={i}
                  count={f.nodeTypes.length}
                  label="le type de nœud"
                  onmove={(delta) => moveItem(f.nodeTypes, i, delta)}
                  onremove={() => f.nodeTypes.splice(i, 1)}
                />
              {/if}
            </div>
          {:else}
            <p class="empty">Aucun type de nœud.</p>
          {/each}
          {#if !d.readonly}
            <button type="button" class="small" onclick={() => f.nodeTypes.push(emptyNodeType())}>+ Type de nœud</button>
          {/if}
          <p class="hint cols">Colonnes : nom · description · propriétés (séparées par des virgules).</p>

          <h4 class="sub" data-path="linkTypes">Types de liens</h4>
          {#each f.linkTypes as l, i}
            <div class="item" class:has-issues={d.count(`linkTypes[${i}]`) > 0} data-path="linkTypes[{i}]">
              <input
                type="text"
                class="mono"
                aria-label="Nom du type de lien"
                bind:value={l.name}
                class:bad={d.bad(`linkTypes[${i}].name`)}
                data-path="linkTypes[{i}].name"
                placeholder="verifies"
              />
              <select aria-label="De" bind:value={l.from} class:bad={d.bad(`linkTypes[${i}].from`)} data-path="linkTypes[{i}].from">
                <option value="">— de —</option>
                {#if l.from && !d.nodeTypeNames.includes(l.from)}<option value={l.from}>{l.from} (inconnu)</option>{/if}
                {#each d.nodeTypeNames as t (t)}<option value={t}>{t}</option>{/each}
              </select>
              <select aria-label="Vers" bind:value={l.to} class:bad={d.bad(`linkTypes[${i}].to`)} data-path="linkTypes[{i}].to">
                <option value="">— vers —</option>
                {#if l.to && !d.nodeTypeNames.includes(l.to)}<option value={l.to}>{l.to} (inconnu)</option>{/if}
                {#each d.nodeTypeNames as t (t)}<option value={t}>{t}</option>{/each}
              </select>
              {#if !d.readonly}
                <RowTools
                  index={i}
                  count={f.linkTypes.length}
                  label="le type de lien"
                  onmove={(delta) => moveItem(f.linkTypes, i, delta)}
                  onremove={() => f.linkTypes.splice(i, 1)}
                />
              {/if}
            </div>
          {:else}
            <p class="empty">Aucun type de lien.</p>
          {/each}
          {#if !d.readonly}
            <button type="button" class="small" onclick={() => f.linkTypes.push(emptyLinkType())}>+ Type de lien</button>
          {/if}
        </section>

        <section class="card">
          <h3>Contenu</h3>
          <div class="content">
            {#each SECTIONS as s (s)}
              <div class="col" data-path={s}>
                <div class="col-head">
                  <Icon name={SECTION_ICON[s]} size={14} />
                  <strong>{SECTION_LABEL[s]}</strong>
                  <span class="hint">{f[s].length}</span>
                  <span class="grow"></span>
                  {#if !d.readonly}
                    <button type="button" class="small ghost" onclick={() => add(s)} aria-label={`Ajouter : ${SECTION_LABEL[s]}`}>+</button>
                  {/if}
                </div>
                <ul>
                  {#each f[s] as it, i (it.uid)}
                    {@const n = d.count(`${s}[${i}]`)}
                    <li>
                      <button
                        type="button"
                        class="link"
                        onclick={() => openItem(d, s, it)}
                        ondblclick={() => openItem(d, s, it, true)}>{it.name || '(sans nom)'}</button
                      >
                      <span class="hint ell">{summary(s, it)}</span>
                      {#if n}<span class="count">{n}</span>{/if}
                    </li>
                  {:else}
                    <li class="empty">{s === 'agents' ? 'Agent par défaut (toutes les actions)' : 'Aucun'}</li>
                  {/each}
                </ul>
              </div>
            {/each}
          </div>
        </section>
      {/if}
    </fieldset>
  {/if}
</div>

<style>
  h4.sub {
    margin-top: 1rem;
  }
  .item {
    display: grid;
    grid-template-columns: minmax(120px, 1fr) minmax(140px, 1.5fr) minmax(140px, 1.3fr) auto;
    gap: 0.4rem;
    align-items: center;
    margin-bottom: 0.3rem;
    border-radius: var(--radius-sm);
  }
  .item.has-issues {
    box-shadow: inset 3px 0 0 var(--danger);
    padding-left: 5px;
  }
  .cols {
    margin: 0.3rem 0 0;
  }
  .content {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 0.8rem;
  }
  .col-head {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    border-bottom: 1px solid var(--border);
    padding-bottom: 0.2rem;
    margin-bottom: 0.3rem;
  }
  .col ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.15rem;
  }
  .col li {
    display: flex;
    gap: 0.4rem;
    align-items: baseline;
    min-width: 0;
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
  @media (max-width: 800px) {
    .item {
      grid-template-columns: 1fr;
    }
  }
</style>
