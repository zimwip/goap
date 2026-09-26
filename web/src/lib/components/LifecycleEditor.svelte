<script lang="ts">
  // One lifecycle of a domain (ADR 0014): states (editable / final) and transitions.
  import { moveItem, type LifecycleForm } from '../methodologyForm';
  import RowTools from './RowTools.svelte';

  let {
    lc = $bindable(),
    readonly = false,
    documents = false,
    guardInstances = [],
    actionInstances = [],
    bad = () => false,
    path,
  }: {
    lc: LifecycleForm;
    readonly?: boolean;
    /** show the children rule (some node type using this lifecycle is a document) */
    documents?: boolean;
    /** names of the transition_guard / transition_action instances of the domain */
    guardInstances?: string[];
    actionInstances?: string[];
    /** does an issue exist at this path? */
    bad?: (path: string) => boolean;
    path: string;
  } = $props();

  const states = $derived(lc.states.map((s) => s.name).filter(Boolean));

  function addState() {
    lc.states.push({ name: '', description: '', editable: false, final: false });
  }
  function addTransition() {
    lc.transitions.push({ name: '', from: states[0] ?? '', to: states[1] ?? '', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] });
  }
</script>

<div class="lc" data-path={path}>
  <div class="field">
    <label for="{path}-initial">Initial state <span class="hint">(state of the nodes a change creates)</span></label>
    <select id="{path}-initial" bind:value={lc.initial} disabled={readonly}>
      <option value="">— choose —</option>
      {#each states as s (s)}<option value={s}>{s}</option>{/each}
    </select>
  </div>

  <h5>States</h5>
  {#each lc.states as s, i}
    <div class="srow">
      <input type="text" class="mono" aria-label="State name" bind:value={s.name} placeholder="draft" disabled={readonly} />
      <input type="text" aria-label="Description" bind:value={s.description} placeholder="Description" disabled={readonly} />
      <label class="check" title="A working state: only held through a change, never persisted"><input type="checkbox" bind:checked={s.editable} disabled={readonly} /> editable</label>
      <label class="check" title="No way out"><input type="checkbox" bind:checked={s.final} disabled={readonly} /> final</label>
      {#if !readonly}<button type="button" class="small ghost" aria-label="Remove the state" onclick={() => lc.states.splice(i, 1)}>×</button>{/if}
    </div>
  {/each}
  {#if !readonly}<button type="button" class="small" onclick={addState}>+ State</button>{/if}

  <h5>Transitions</h5>
  {#each lc.transitions as t, i}
    <div class="trow">
      <div class="line">
        <input type="text" class="mono" aria-label="Transition name" bind:value={t.name} placeholder="approve" disabled={readonly} />
        <select aria-label="From" bind:value={t.from} disabled={readonly}>
          <option value="">— from —</option>
          {#each states as s (s)}<option value={s}>{s}</option>{/each}
        </select>
        <span class="arrow">→</span>
        <select aria-label="To" bind:value={t.to} disabled={readonly}>
          <option value="">— to —</option>
          {#each states as s (s)}<option value={s}>{s}</option>{/each}
        </select>
        {#if !readonly}<button type="button" class="small ghost" aria-label="Remove the transition" onclick={() => lc.transitions.splice(i, 1)}>×</button>{/if}
      </div>
      <div class="line opts">
        <input type="text" class="mono" aria-label="Permission" bind:value={t.permission} placeholder="permission (resource:action)" disabled={readonly} />
        <input type="text" class="mono" aria-label="Required attributes" bind:value={t.requiresAttributes} placeholder="required attributes" disabled={readonly} />
        <input type="text" class="mono" aria-label="Required outgoing links" bind:value={t.requiresLinks} placeholder="required outgoing links" disabled={readonly} />
        {#if documents || t.children}<input type="text" class="mono" aria-label="Allowed states of the children" bind:value={t.children} placeholder="children must be in…" disabled={readonly} />{/if}
      </div>
      {#each [{ key: 'guards', label: 'Guard algorithms', hint: 'run in order after the CEL guard; all must accept', names: guardInstances }, { key: 'actions', label: 'Action algorithms', hint: 'run in order once the transition is accepted', names: actionInstances }] as kind (kind.key)}
        {@const list = t[kind.key as 'guards' | 'actions']}
        {#if list.length || !readonly}
          <div class="plugs" data-path="{path}.transitions[{i}].{kind.key}">
            <span class="plabel">{kind.label} <span class="hint">({kind.hint})</span></span>
            {#each list as inst, k}
              <div class="line">
                <select aria-label={kind.label} bind:value={list[k]} class:bad={bad(`${path}.transitions[${i}].${kind.key}[${k}]`)} data-path="{path}.transitions[{i}].{kind.key}[{k}]" disabled={readonly}>
                  <option value="">— choose —</option>
                  {#if inst && !kind.names.includes(inst)}<option value={inst}>{inst} (unknown)</option>{/if}
                  {#each kind.names as nm (nm)}<option value={nm}>{nm}</option>{/each}
                </select>
                {#if !readonly}<RowTools index={k} count={list.length} label="the algorithm" onmove={(delta) => moveItem(list, k, delta)} onremove={() => list.splice(k, 1)} />{/if}
              </div>
            {/each}
            {#if !readonly}
              <button type="button" class="small" disabled={!kind.names.length} title={kind.names.length ? '' : 'Create an instance of this type in the Algorithms section first'} onclick={() => list.push('')}>+ {kind.key === 'guards' ? 'Guard' : 'Action'}</button>
            {/if}
          </div>
        {/if}
      {/each}
      <input type="text" class="mono guard" aria-label="Guard" bind:value={t.guard} placeholder={'guard (CEL): node.props.title != "" && children.all(c, c.state == "approved")'} disabled={readonly} />
    </div>
  {/each}
  {#if !readonly}<button type="button" class="small" onclick={addTransition}>+ Transition</button>{/if}
</div>

<style>
  .lc {
    display: grid;
    gap: 0.4rem;
  }
  h5 {
    margin: 0.6rem 0 0.1rem;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
  }
  .srow,
  .line {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .srow input[type='text']:first-child {
    width: 10rem;
    flex: none;
  }
  .srow input[type='text']:nth-child(2) {
    flex: 1;
    min-width: 8rem;
    width: auto;
  }
  .line select,
  .line input[type='text']:first-child {
    width: auto;
    min-width: 9rem;
  }
  .trow {
    display: grid;
    gap: 0.25rem;
    padding: 0.3rem 0.4rem;
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .opts input {
    flex: 1;
    min-width: 10rem;
  }
  .guard {
    width: 100%;
  }
  .plugs {
    display: grid;
    gap: 0.2rem;
    justify-items: start;
  }
  .plabel {
    font-size: 0.8rem;
    color: var(--muted);
  }
  .arrow {
    color: var(--muted);
  }
  .check {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    font-weight: 400;
  }
</style>
