<script lang="ts">
  // Onglet « action » : édite une action du brouillon de sa méthodologie.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import ActionEditor from '../../components/ActionEditor.svelte';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { draftOf, draftActions, removeItemAction, syncTabUid, openItem } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const index = $derived(d.indexOf('actions', tab.params.uid ?? '', tab.params.name ?? ''));
  const item = $derived(index >= 0 ? d.form.actions[index] : undefined);
  let root = $state<HTMLElement>();

  $effect(() => syncTabUid(tab, 'actions', item));
  provideActions(
    () => tab.id,
    () => draftActions(d, removeItemAction(d, 'actions', tab, () => index)),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  const agents = $derived(
    item ? d.form.agents.filter((a) => a.actions.length === 0 || a.actions.includes(item.name)) : [],
  );
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} icon="zap" kind="Action" title={item.name || '(sans nom)'} dirty={d.itemDirty('actions', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <ActionEditor
          bind:action={d.form.actions[index]}
          {index}
          conditions={d.conditionOptions}
          nodeTypes={d.nodeTypeNames}
          linkTypes={d.linkTypeNames}
          bad={d.bad}
          readonly={d.readonly}
          onrename={(from, to) => d.rename('actions', from, to)}
        />
      </section>
    </fieldset>
    <p class="hint">
      Agents pouvant l'utiliser :
      {#each agents as a, i (a.uid)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'agents', a)}
          >{a.name || '(sans nom)'}</button
        >{:else}
        {d.form.agents.length ? 'aucun' : 'agent par défaut'}
      {/each}
    </p>
  {:else}
    <ItemMissing draft={d} what="Action" />
  {/if}
</div>
