<script lang="ts">
  // "Action" tab: edits an action in its methodology's draft.
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

  const actionNames = $derived([...new Set(d.form.actions.map((a) => a.name.trim()).filter(Boolean))]);
  /** local specializations of this action */
  const specializations = $derived(
    item && item.name.trim()
      ? d.form.actions.filter((a) => {
          const s = a.specializes.trim();
          return a !== item && (s === item.name.trim() || s === `${d.form.name.trim()}/${item.name.trim()}`);
        })
      : [],
  );
  const specialized = $derived(
    item?.specializes.trim() && !item.specializes.includes('/')
      ? d.form.actions.find((a) => a.name.trim() === item.specializes.trim())
      : undefined,
  );

  const agents = $derived(
    item ? d.form.agents.filter((a) => a.actions.length === 0 || a.actions.includes(item.name)) : [],
  );
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} icon="zap" kind="Action" title={item.name || '(unnamed)'} dirty={d.itemDirty('actions', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <ActionEditor
          bind:action={d.form.actions[index]}
          {index}
          conditions={d.conditionOptions}
          nodeTypes={d.nodeTypeNames}
          linkTypes={d.linkTypeNames}
          actions={actionNames}
          bad={d.bad}
          readonly={d.readonly}
          onrename={(from, to) => d.rename('actions', from, to)}
        />
      </section>
    </fieldset>
    {#if specialized}
      <p class="hint">
        Specializes <button type="button" class="link" onclick={() => openItem(d, 'actions', specialized)}>{specialized.name}</button>.
      </p>
    {/if}
    {#if specializations.length || item.kind === 'abstract'}
      <p class="hint">
        Specializations:
        {#each specializations as a, i (a.uid)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'actions', a)}
            >{a.name || '(unnamed)'}</button
          >{a.priority ? ` (priority ${a.priority})` : ''}{:else}
          none in this methodology{item.kind === 'abstract' ? ' — an abstract action must be specialized' : ''}
        {/each}
      </p>
    {/if}
    <p class="hint">
      Agents that can use it:
      {#each agents as a, i (a.uid)}{i ? ', ' : ''}<button type="button" class="link" onclick={() => openItem(d, 'agents', a)}
          >{a.name || '(unnamed)'}</button
        >{:else}
        {d.form.agents.length ? 'none' : 'default agent'}
      {/each}
    </p>
  {:else}
    <ItemMissing draft={d} what="Action" />
  {/if}
</div>
