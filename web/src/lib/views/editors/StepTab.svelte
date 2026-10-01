<script lang="ts">
  // "Step" tab (ADR 0034): one step of a process or a method (a part of it, by composition) with its inputs and
  // outputs, how it is done, and — level by level — whether its sub-steps and its siblings chain from the inputs of
  // their parent to its outputs.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import StepEditor from './StepEditor.svelte';
  import DraftFlow from '../../components/DraftFlow.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { closeWhere } from '../../shell/tabs.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';
  import { draftOf, draftActions, findStep, addSubStep, openItem, openStep } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const loc = $derived(findStep(d, tab.params.skey ?? ''));
  const index = $derived(loc ? loc.siblings.indexOf(loc.step) : -1);
  const p = $derived(loc ? `${loc.section}[${d.indexOf(loc.section, loc.owner.uid, loc.owner.name)}].${loc.at}` : '');
  // a step has a level of its own when it has sub-steps, an agent operating it, or methods that specialize it (an action is not broken down)
  const hasLevel = $derived(!!loc && (loc.step.steps.length > 0 || ['agent', 'capability'].includes(loc.step.method)));
  const root = $derived(loc?.owner.name ?? '');
  const parentPath = $derived(loc ? loc.path.slice(0, loc.path.lastIndexOf('/')) : '');
  let rootEl = $state<HTMLElement>();

  function openStepAt(path: string) {
    if (!loc) return;
    let list = loc.owner.steps;
    let found;
    for (const n of path.split('/').slice(1)) {
      found = list.find((x) => x.name === n);
      if (!found) return;
      list = found.steps;
    }
    if (found) openStep(d, found, loc.owner, true);
  }

  // the tab follows the step's name
  $effect(() => {
    if (loc && tab.params.name !== loc.step.name) tab.params.name = loc.step.name;
  });
  provideActions(
    () => tab.id,
    () =>
      draftActions(
        d,
        d.readonly || !loc
          ? []
          : [
              { id: 'subStep', label: 'New sub-step', icon: 'plus', run: () => addSubStep(d, loc.owner, loc.step) },
              {
                id: 'removeStep',
                label: 'Delete the step',
                icon: 'trash',
                danger: true,
                run: async () => {
                  if (!loc || !(await confirmDialog({ message: `Delete the step "${loc.step.name || 'unnamed'}" and its sub-steps?`, danger: true }))) return;
                  loc.siblings.splice(loc.siblings.indexOf(loc.step), 1);
                  closeWhere((t) => t.id === tab.id);
                },
              },
            ],
      ),
  );
  useReveal(
    () => tab.id,
    () => rootEl,
  );
</script>

<div class="editor-page" bind:this={rootEl}>
  {#if loc}
    <DraftHeader draft={d} icon="node" kind={loc.section === 'methods' ? 'Method step' : 'Step'} title={loc.step.name || '(unnamed)'} dirty={d.itemDirty(loc.section, loc.owner.uid)} />
    <p class="hint">
      In <button type="button" class="link" onclick={() => openItem(d, loc.section, loc.owner)}>{loc.owner.name || '(unnamed)'}</button>
      {#if parentPath !== loc.owner.name}· under <code>{parentPath}</code>{/if}
    </p>
    <section class="card">
      <h3>Flow of {loc.step.name}</h3>
      <DraftFlow draft={d} {root} at={hasLevel ? loc.path : parentPath} only={hasLevel ? '' : loc.path} focus={loc.path} onstep={openStepAt} />
    </section>
    <fieldset class="plain" disabled={d.readonly}>
      <StepEditor bind:step={loc.siblings[index]} siblings={loc.siblings} index={Math.max(index, 0)} path={p} draft={d} />
    </fieldset>
  {:else}
    <ItemMissing draft={d} what="Step" />
  {/if}
</div>
