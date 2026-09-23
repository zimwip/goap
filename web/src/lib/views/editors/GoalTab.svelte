<script lang="ts">
  // "Goal" tab: conditions to reach, value, intent examples.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import CondRows from '../../components/CondRows.svelte';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { draftOf, draftActions, removeItemAction, syncTabUid, openItem } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const index = $derived(d.indexOf('goals', tab.params.uid ?? '', tab.params.name ?? ''));
  const item = $derived(index >= 0 ? d.form.goals[index] : undefined);
  const p = $derived(`goals[${index}]`);
  let root = $state<HTMLElement>();
  let nameAtFocus = '';

  $effect(() => syncTabUid(tab, 'goals', item));
  provideActions(
    () => tab.id,
    () => draftActions(d, removeItemAction(d, 'goals', tab, () => index)),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  const agents = $derived(item ? d.form.agents.filter((a) => a.goals.length === 0 || a.goals.includes(item.name)) : []);
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} icon="target" kind="Goal" title={item.name || '(unnamed)'} dirty={d.itemDirty('goals', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="g-name">Name</label>
            <input
              id="g-name"
              type="text"
              class="mono"
              bind:value={item.name}
              class:bad={d.bad(`${p}.name`)}
              data-path="{p}.name"
              placeholder="impact_report"
              onfocus={() => (nameAtFocus = item.name)}
              onchange={() => {
                if (nameAtFocus && nameAtFocus !== item.name) d.rename('goals', nameAtFocus, item.name);
                nameAtFocus = item.name;
              }}
            />
          </div>
          <div class="field">
            <label for="g-value">Value</label>
            <input id="g-value" type="number" step="any" bind:value={item.value} class:bad={d.bad(`${p}.value`)} data-path="{p}.value" />
          </div>
        </div>
        <div class="field">
          <label for="g-desc">Description</label>
          <input id="g-desc" type="text" bind:value={item.description} class:bad={d.bad(`${p}.description`)} data-path="{p}.description" />
        </div>
        <div class="grid2">
          <div class="field">
            <label for="g-ex">Intent examples <span class="opt">(one per line)</span></label>
            <textarea id="g-ex" rows="5" bind:value={item.examples} class:bad={d.bad(`${p}.examples`)} data-path="{p}.examples"></textarea>
          </div>
          <div class="field">
            <CondRows
              bind:rows={item.pre}
              options={d.conditionOptions}
              path="{p}.pre"
              label="Conditions to reach"
              bad={d.bad}
              readonly={d.readonly}
            />
          </div>
        </div>
      </section>
    </fieldset>
    <p class="hint">
      Agents targeting this goal:
      {#each agents as a, i (a.uid)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'agents', a)}
          >{a.name || '(unnamed)'}</button
        >{:else}
        {d.form.agents.length ? 'none' : 'default agent'}
      {/each}
    </p>
  {:else}
    <ItemMissing draft={d} what="Goal" />
  {/if}
</div>
