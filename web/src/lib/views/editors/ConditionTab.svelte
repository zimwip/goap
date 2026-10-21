<script lang="ts">
  // "Condition" tab: CEL expression evaluated over the blackboard.
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

  // a scoped condition (ADR 0109): evaluated per subject (`self`); an aggregate: every / some / no subject satisfies one
  const SCOPES = ['impact', 'item', 'risk', 'action', 'step'];
  const aggregate = $derived(item?.all ? 'all' : item?.any ? 'any' : item?.none ? 'none' : '');
  const aggregated = $derived(item ? item.all || item.any || item.none : '');
  const scopedNames = $derived(d.form.conditions.filter((c) => c.scope && c.uid !== item?.uid).map((c) => c.name));
  function setAggregate(op: string, of: string) {
    if (!item) return;
    item.all = op === 'all' ? of : '';
    item.any = op === 'any' ? of : '';
    item.none = op === 'none' ? of : '';
    if (op !== 'all') item.empty = false;
  }

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
    <DraftHeader tabId={tab.id} draft={d} path={`conditions[${index}]`} icon="branch" kind="Condition" title={item.name || '(unnamed)'} dirty={d.itemDirty('conditions', item.uid)} />
    <fieldset class="plain" disabled={d.readonly}>
      <section class="card">
        <div class="grid">
          <div class="field">
            <label for="c-name">Name</label>
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
        <div class="grid">
          <div class="field">
            <label for="c-scope">Scope</label>
            <select id="c-scope" bind:value={item.scope} class:bad={d.bad(`${p}.scope`)} data-path="{p}.scope" disabled={!!aggregate}>
              <option value="">the change</option>
              {#each SCOPES as sc (sc)}<option value={sc}>each {sc} (self)</option>{/each}
            </select>
          </div>
          <div class="field">
            <label for="c-agg">Aggregate</label>
            <div class="row">
              <select id="c-agg" value={aggregate} onchange={(e) => setAggregate(e.currentTarget.value, aggregated)} disabled={!!item.scope}>
                <option value="">none</option>
                <option value="all">all subjects satisfy</option>
                <option value="any">some subject satisfies</option>
                <option value="none">no subject satisfies</option>
              </select>
              {#if aggregate}
                <select value={aggregated} onchange={(e) => setAggregate(aggregate, e.currentTarget.value)} class:bad={d.bad(`${p}.${aggregate}`)} data-path="{p}.{aggregate}">
                  {#if aggregated && !scopedNames.includes(aggregated)}<option value={aggregated}>{aggregated}</option>{/if}
                  <option value="" disabled>scoped condition…</option>
                  {#each scopedNames as n (n)}<option value={n}>{n}</option>{/each}
                </select>
              {/if}
              {#if aggregate === 'all'}<label class="inline"><input type="checkbox" bind:checked={item.empty} /> true over no subject</label>{/if}
            </div>
          </div>
        </div>
        {#if item.scope || aggregate}
          <div class="field">
            <label for="c-where">Subjects (where)</label>
            <CodeEditor id="c-where" bind:value={item.where} language="cel" readonly={d.readonly} label="Subjects" minHeight="2rem" placeholder="optional: narrows the subjects, `self` the subject" bad={d.bad(`${p}.where`)} path="{p}.where" />
          </div>
        {/if}
        {#if !aggregate}
        <div class="field">
          <label for="c-expr">CEL expression</label>
          <CodeEditor
            id="c-expr"
            bind:value={item.expr}
            language="cel"
            readonly={d.readonly}
            label="CEL expression"
            minHeight="4rem"
            placeholder="size(impacts) > 0"
            bad={d.bad(`${p}.expr`)}
            path="{p}.expr"
          />
          <p class="hint">
            Variables: <code>impacts</code>, <code>proposals</code>, <code>artifacts</code>, <code>items</code>,
            <code>change</code>…{#if item.scope} <code>self</code> is the {item.scope} judged.{/if} Conditions form the planner's world state.
          </p>
        </div>
        {/if}
      </section>
    </fieldset>
    <section class="card">
      <h3>Used by</h3>
      {#if usedBy.actions.length || usedBy.goals.length}
        <ul class="chips">
          {#each usedBy.actions as a (a.uid)}
            <li><button type="button" class="chip" onclick={() => openItem(d, 'actions', a)}>action · {a.name}</button></li>
          {/each}
          {#each usedBy.goals as g (g.uid)}
            <li><button type="button" class="chip" onclick={() => openItem(d, 'goals', g)}>goal · {g.name}</button></li>
          {/each}
        </ul>
      {:else}
        <p class="empty">No action or goal references it.</p>
      {/if}
    </section>
  {:else}
    <ItemMissing draft={d} what="Condition" />
  {/if}
</div>

<style>
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  label.inline {
    display: flex;
    gap: 4px;
    align-items: center;
    font-weight: normal;
  }
  button.chip {
    cursor: pointer;
    min-height: 0;
    font-weight: 500;
  }
</style>
