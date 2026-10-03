<script lang="ts">
  // "Agent" tab: planner, eligible actions and goals.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import PickList from './PickList.svelte';
  import TriggersEditor from './TriggersEditor.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { PLANNERS, LLM_PLANNERS } from '../../methodologyForm';
  import { tools, refreshTools, mcpsOfScope } from '../../stores/tools.svelte';
  import { draftOf, draftActions, removeItemAction, syncTabUid } from './methodologyTabs';
  import ModelAliasField from '../../components/ModelAliasField.svelte';
  import { modelChoices, refreshModelChoices } from '../../stores/modelChoices.svelte';

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
    goap: 'GOAP — A* plan over preconditions and effects',
    utility: 'Utility — at each step, the highest-utility action',
    hybrid: 'Hybrid — GOAP plan tie-broken by utility',
    llm: 'LLM — the model picks the next action directly',
    'llm-scoring': 'LLM scoring — the model scores utility, GOAP plan tie-broken by it',
  };

  const needsModel = $derived(!!item && (LLM_PLANNERS as readonly string[]).includes(item.planner));

  $effect(() => {
    if (needsModel && !modelChoices.loaded) void refreshModelChoices();
  });

  const actionNames = $derived([...new Set(d.form.actions.map((a) => a.name.trim()).filter(Boolean))]);
  const goalNames = $derived([...new Set(d.form.goals.map((g) => g.name.trim()).filter(Boolean))]);
  const actionDetails = $derived(Object.fromEntries(d.form.actions.map((a) => [a.name, `${a.kind}${a.utility ? ' · utility' : ''}`])));
  const goalDetails = $derived(Object.fromEntries(d.form.goals.map((g) => [g.name, g.description])));
  const noUtility = $derived(
    item && item.planner !== 'goap'
      ? d.form.actions.filter((a) => (item.actions.length === 0 || item.actions.includes(a.name)) && !a.utility.trim()).map((a) => a.name)
      : [],
  );
  /** MCPs only actions may declare (ADR 0028) */
  const actionOnly = $derived(mcpsOfScope('action'));
  $effect(() => {
    if (!tools.loaded) void refreshTools();
  });
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} path={`agents[${index}]`} icon="bot" kind="Agent" title={item.name || '(unnamed)'} dirty={d.itemDirty('agents', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="ag-name">Name <span class="opt">(lowercase, digits, - or _)</span></label>
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
            <label for="ag-planner">Planner</label>
            <select id="ag-planner" bind:value={item.planner} class:bad={d.bad(`${p}.planner`)} data-path="{p}.planner">
              {#each PLANNERS as pl (pl)}<option value={pl}>{PLANNER_LABELS[pl]}</option>{/each}
            </select>
          </div>
          {#if needsModel}
            <div class="field">
              <label for="ag-model">Model <span class="opt">(this agent's planner calls it each planning cycle)</span></label>
              <ModelAliasField id="ag-model" bind:value={item.model} bad={d.bad(`${p}.model`)} disabled={d.readonly} />
            </div>
          {/if}
        </div>
        <div class="field">
          <label for="ag-desc">Description <span class="opt">(used to identify the agent from an intent)</span></label>
          <textarea id="ag-desc" rows="2" bind:value={item.description} class:bad={d.bad(`${p}.description`)} data-path="{p}.description"
          ></textarea>
        </div>
        <div class="field">
          <label for="ag-ex">Intent examples <span class="opt">(one per line)</span></label>
          <textarea id="ag-ex" rows="4" bind:value={item.examples} class:bad={d.bad(`${p}.examples`)} data-path="{p}.examples"></textarea>
        </div>
        <div class="field">
          <label for="ag-mcps">MCPs <span class="opt">(comma separated)</span></label>
          <input
            id="ag-mcps"
            type="text"
            class="mono"
            bind:value={item.mcps}
            class:bad={d.bad(`${p}.mcps`)}
            data-path="{p}.mcps"
            placeholder="document-repository"
          />
          <span class="hint"
            >MCPs whose tools this agent's LLM/script actions may use, in addition to the ones the actions declare. An MCP of scope agent (orchestration: <code>goap-scheduler</code> starts other agents) is declared here only and reached by the llm actions{#if actionOnly.length}; action only, not here: <code>{actionOnly.join(', ')}</code>{/if}.</span
          >
        </div>
        <div class="field">
          <label for="ag-roles">Roles <span class="opt">(comma separated, declared by the methodology)</span></label>
          <input
            id="ag-roles"
            type="text"
            class="mono"
            bind:value={item.roles}
            class:bad={d.bad(`${p}.roles`)}
            data-path="{p}.roles"
            placeholder={(d.form.roles ?? []).map((r) => r.name).join(', ') || 'developer'}
          />
          <span class="hint"
            >Who may run this agent: someone holding one of these roles on the project of the change (ADR 0043). Its actions that name no roles of their own need one too. Empty: any member of the project.</span
          >
        </div>
        {#if noUtility.length}
          <div class="alert warn">
            Planner {item.planner}: these actions have no utility expression —
            {noUtility.join(', ')}.
          </div>
        {/if}
      </section>
      <div class="grid2">
        <PickList
          bind:selected={item.actions}
          options={actionNames}
          details={actionDetails}
          allLabel="all actions"
          label="Eligible actions"
          path="{p}.actions"
          bad={d.bad}
          readonly={d.readonly}
        />
        <PickList
          bind:selected={item.goals}
          options={goalNames}
          details={goalDetails}
          allLabel="all goals"
          label="Goals"
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
