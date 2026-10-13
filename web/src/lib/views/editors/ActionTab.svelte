<script lang="ts">
  // "Action" tab: edits an action in its methodology's draft.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import ActionEditor from '../../components/ActionEditor.svelte';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import SubscriptionsEditor from './SubscriptionsEditor.svelte';
  import IssueHints from '../../components/IssueHints.svelte';
  import { declaredNames } from '../../subscriptions';
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
  // the declared events the action may raise (ADR 0110)
  const declared = $derived(declaredNames(d.form.events));
  const emits = $derived(item ? item.emits.split(',').map((x) => x.trim()).filter(Boolean) : []);
  function toggleEmit(name: string) {
    if (!item) return;
    item.emits = (emits.includes(name) ? emits.filter((x) => x !== name) : [...emits, name]).join(', ');
  }
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} path={`actions[${index}]`} icon="zap" kind="Action" title={item.name || '(unnamed)'} dirty={d.itemDirty('actions', item.uid)} />
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
      <section class="card" data-path="actions[{index}].on">
        <h3>Reactions</h3>
        <SubscriptionsEditor bind:subs={item.on} draft={d} path={`actions[${index}]`} label="Reacts to" hint="the action runs alone towards its effects on each of them (ADR 0110)" />
        <div class="label">Emits <span class="opt">(events of the methodology it raises when it reaches its effects)</span></div>
        <div class="emits" data-path="actions[{index}].emits">
          {#each declared as name (name)}
            <label class="check"><input type="checkbox" checked={emits.includes(name)} onchange={() => toggleEmit(name)} /> <span class="mono">{name}</span></label>
          {:else}
            <span class="hint">The methodology declares no event (Events, on the methodology).</span>
          {/each}
          {#each emits.filter((x) => !declared.includes(x)) as name (name)}
            <label class="check bad"><input type="checkbox" checked onchange={() => toggleEmit(name)} /> <span class="mono">{name}</span> (unknown)</label>
          {/each}
        </div>
        <IssueHints draft={d} path="actions[{index}].emits" />
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

<style>
  .emits {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
  }
  .check {
    display: flex;
    gap: 4px;
    align-items: center;
  }
  .check.bad {
    color: var(--danger);
  }
</style>
