<script lang="ts">
  // Onglet « condition » : expression CEL évaluée sur le tableau noir.
  import { untrack } from 'svelte';
  import type { Tab } from '../../shell/types';
  import CodeEditor from '../../components/CodeEditor.svelte';
  import DraftHeader from './DraftHeader.svelte';
  import ItemMissing from './ItemMissing.svelte';
  import { provideActions, useReveal } from '../../shell/workbench.svelte';
  import { draftOf, draftActions, removeItemAction, syncTabUid, openItem } from './methodologyTabs';

  let { tab }: { tab: Tab } = $props();

  const d = untrack(() => draftOf(tab));
  const index = $derived(d.indexOf('conditions', tab.params.uid ?? '', tab.params.name ?? ''));
  const item = $derived(index >= 0 ? d.form.conditions[index] : undefined);
  const p = $derived(`conditions[${index}]`);
  let root = $state<HTMLElement>();
  let nameAtFocus = '';

  $effect(() => syncTabUid(tab, 'conditions', item));
  provideActions(
    () => tab.id,
    () => draftActions(d, removeItemAction(d, 'conditions', tab, () => index)),
  );
  useReveal(
    () => tab.id,
    () => root,
  );

  const usedBy = $derived.by(() => {
    const n = item?.name;
    if (!n) return { actions: [], goals: [] };
    return {
      actions: d.form.actions.filter((a) => a.pre.some((r) => r.cond === n) || a.effects.some((r) => r.cond === n)),
      goals: d.form.goals.filter((g) => g.pre.some((r) => r.cond === n)),
    };
  });
</script>

<div class="editor-page" bind:this={root}>
  {#if item}
    <DraftHeader draft={d} icon="branch" kind="Condition" title={item.name || '(sans nom)'} dirty={d.itemDirty('conditions', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="c-name">Nom</label>
            <input
              id="c-name"
              type="text"
              class="mono"
              bind:value={item.name}
              class:bad={d.bad(`${p}.name`)}
              data-path="{p}.name"
              placeholder="has_impacts"
              onfocus={() => (nameAtFocus = item.name)}
              onchange={() => {
                if (nameAtFocus && nameAtFocus !== item.name) d.rename('conditions', nameAtFocus, item.name);
                nameAtFocus = item.name;
              }}
            />
          </div>
          <div class="field">
            <label for="c-desc">Description</label>
            <input id="c-desc" type="text" bind:value={item.description} class:bad={d.bad(`${p}.description`)} data-path="{p}.description" />
          </div>
        </div>
        <div class="field">
          <label for="c-expr">Expression CEL</label>
          <CodeEditor
            id="c-expr"
            bind:value={item.expr}
            language="cel"
            readonly={d.readonly}
            label="Expression CEL"
            minHeight="4rem"
            placeholder="size(impacts) > 0"
            bad={d.bad(`${p}.expr`)}
            path="{p}.expr"
          />
          <p class="hint">
            Variables : <code>impacts</code>, <code>proposals</code>, <code>artifacts</code>, <code>items</code>,
            <code>change</code>… Les conditions forment l'état du monde du planificateur.
          </p>
        </div>
      </section>
    </fieldset>
    <section class="card">
      <h3>Utilisée par</h3>
      {#if usedBy.actions.length || usedBy.goals.length}
        <ul class="chips">
          {#each usedBy.actions as a (a.uid)}
            <li><button type="button" class="chip" onclick={() => openItem(d, 'actions', a)}>action · {a.name}</button></li>
          {/each}
          {#each usedBy.goals as g (g.uid)}
            <li><button type="button" class="chip" onclick={() => openItem(d, 'goals', g)}>objectif · {g.name}</button></li>
          {/each}
        </ul>
      {:else}
        <p class="empty">Aucune action ni objectif n'y fait référence.</p>
      {/if}
    </section>
  {:else}
    <ItemMissing draft={d} what="Condition" />
  {/if}
</div>

<style>
  button.chip {
    cursor: pointer;
    min-height: 0;
    font-weight: 500;
  }
</style>
