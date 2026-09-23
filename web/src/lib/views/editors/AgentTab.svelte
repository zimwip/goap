<script lang="ts">
  // Onglet « agent » : planificateur, actions admissibles et objectifs.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import PickList from './PickList.svelte';
  import TriggersEditor from './TriggersEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { PLANNERS } from '../../methodologyForm';
  import { draftOf, draftActions, removeItemAction, syncTabUid } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const index = $derived(d.indexOf('agents', tab.params.uid ?? '', tab.params.name ?? ''));
  const item = $derived(index >= 0 ? d.form.agents[index] : undefined);
  const p = $derived(`agents[${index}]`);
  let root = $state<HTMLElement>();

  $effect(() => syncTabUid(tab, 'agents', item));
  provideActions(
    () => tab.id,
    () => draftActions(d, removeItemAction(d, 'agents', tab, () => index)),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  const PLANNER_LABELS: Record<string, string> = {
    goap: 'GOAP — plan A* sur préconditions et effets',
    utility: "Utility — à chaque pas, l'action d'utilité maximale",
    hybrid: "Hybride — plan GOAP départagé par l'utilité",
  };

  const actionNames = $derived([...new Set(d.form.actions.map((a) => a.name.trim()).filter(Boolean))]);
  const goalNames = $derived([...new Set(d.form.goals.map((g) => g.name.trim()).filter(Boolean))]);
  const actionDetails = $derived(Object.fromEntries(d.form.actions.map((a) => [a.name, `${a.kind}${a.utility ? ' · utilité' : ''}`])));
  const goalDetails = $derived(Object.fromEntries(d.form.goals.map((g) => [g.name, g.description])));
  const noUtility = $derived(
    item && item.planner !== 'goap'
      ? d.form.actions.filter((a) => (item.actions.length === 0 || item.actions.includes(a.name)) && !a.utility.trim()).map((a) => a.name)
      : [],
  );
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} icon="bot" kind="Agent" title={item.name || '(sans nom)'} dirty={d.itemDirty('agents', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="ag-name">Nom <span class="opt">(minuscules, chiffres, - ou _)</span></label>
            <input
              id="ag-name"
              type="text"
              class="mono"
              bind:value={item.name}
              class:bad={d.bad(`${p}.name`)}
              data-path="{p}.name"
              placeholder="impact-analyst"
            />
          </div>
          <div class="field">
            <label for="ag-planner">Planificateur</label>
            <select id="ag-planner" bind:value={item.planner} class:bad={d.bad(`${p}.planner`)} data-path="{p}.planner">
              {#each PLANNERS as pl (pl)}<option value={pl}>{PLANNER_LABELS[pl]}</option>{/each}
            </select>
          </div>
        </div>
        <div class="field">
          <label for="ag-desc">Description <span class="opt">(sert à identifier l'agent à partir d'une intention)</span></label>
          <textarea id="ag-desc" rows="2" bind:value={item.description} class:bad={d.bad(`${p}.description`)} data-path="{p}.description"
          ></textarea>
        </div>
        <div class="field">
          <label for="ag-ex">Exemples d'intentions <span class="opt">(un par ligne)</span></label>
          <textarea id="ag-ex" rows="4" bind:value={item.examples} class:bad={d.bad(`${p}.examples`)} data-path="{p}.examples"></textarea>
        </div>
        {#if noUtility.length}
          <div class="alert warn">
            Planificateur {item.planner} : ces actions n'ont pas d'expression d'utilité —
            {noUtility.join(', ')}.
          </div>
        {/if}
      </section>
      <div class="grid2">
        <PickList
          bind:selected={item.actions}
          options={actionNames}
          details={actionDetails}
          allLabel="toutes les actions"
          label="Actions admissibles"
          path="{p}.actions"
          bad={d.bad}
          readonly={d.readonly}
        />
        <PickList
          bind:selected={item.goals}
          options={goalNames}
          details={goalDetails}
          allLabel="tous les objectifs"
          label="Objectifs"
          path="{p}.goals"
          bad={d.bad}
          readonly={d.readonly}
        />
      </div>
      <div class="triggers">
        <TriggersEditor
          bind:triggers={item.triggers}
          path="{p}.triggers"
          goals={item.goals.length ? item.goals : goalNames}
          bad={d.bad}
          count={(x) => d.count(x)}
          readonly={d.readonly}
        />
      </div>
    </fieldset>
  {:else}
    <ItemMissing draft={d} what="Agent" />
  {/if}
</div>

<style>
  .triggers {
    margin-top: 0.75rem;
  }
</style>
