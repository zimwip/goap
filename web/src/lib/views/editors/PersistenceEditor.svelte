<script lang="ts">
  // The persistence of an agent or a method (ADR 0112): one instance per scope (the change, or what the event that
  // starts it names), run again for every new execution there with the conversation it had, until conditions close it.
  import type { PersistenceForm } from '../../methodologyForm';
  import CondRows from '../../components/CondRows.svelte';

  let {
    value = $bindable(),
    path,
    options,
    bad,
    readonly = false,
    what = 'agent',
  }: {
    value: PersistenceForm;
    /** path of the agent or method ("agents[0]") */
    path: string;
    /** the conditions of the methodology */
    options: string[];
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
    what?: string;
  } = $props();
  const id = $derived(path.replace(/[^a-z0-9]/gi, '-'));
</script>

<section class="card" data-path="{path}.persistent">
  <h3>Persistence</h3>
  <label class="check">
    <input type="checkbox" bind:checked={value.on} disabled={readonly} />
    Persistent: the same instance of the {what} handles every new execution on its scope, with its earlier exchanges
  </label>
  {#if value.on}
    <div class="grid">
      <div class="field">
        <label for="{id}-scope">Scope <span class="opt">(change, or a path into the event: event.process.methodology)</span></label>
        <input id="{id}-scope" type="text" class="mono" bind:value={value.scope} placeholder="change" class:bad={bad(`${path}.persistent.scope`)} data-path="{path}.persistent.scope" />
      </div>
      <div class="field">
        <label for="{id}-mem">Memory <span class="opt">(exchanges given back to the model; 0: the default)</span></label>
        <input id="{id}-mem" type="number" min="0" max="20" bind:value={value.memory} class:bad={bad(`${path}.persistent.memory`)} data-path="{path}.persistent.memory" />
      </div>
    </div>
    <div class="field">
      <CondRows bind:rows={value.until} {options} path="{path}.persistent.until" label="Closed when a run completes with" {bad} {readonly} />
      <p class="hint">None: the instance lives as long as its change. Once closed, the next execution starts a new instance.</p>
    </div>
  {/if}
</section>

<style>
  .check {
    display: flex;
    gap: 6px;
    align-items: baseline;
  }
</style>
