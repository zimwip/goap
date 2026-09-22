<script lang="ts">
  // Édition d'une action de la méthodologie.
  import { ACTION_KINDS, FOR_EACH, PRODUCE_OPS, type ActionForm } from '../methodologyForm';
  import CondRows from './CondRows.svelte';

  let {
    action = $bindable(),
    index,
    conditions,
    nodeTypes,
    linkTypes,
    bad,
    readonly = false,
  }: {
    action: ActionForm;
    index: number;
    conditions: string[];
    nodeTypes: string[];
    linkTypes: string[];
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
  } = $props();

  const p = $derived(`actions[${index}]`);
  const id = $derived(`act-${index}`);

  const KIND_LABELS: Record<string, string> = {
    llm: 'llm — modèle de langage',
    tool: 'tool — outil externe',
    human: 'human — tâche humaine',
    builtin: 'builtin — fonction intégrée',
  };
  const FOR_EACH_LABELS: Record<string, string> = {
    impacts: 'impacts',
    proposals: 'propositions',
    items: 'items',
    artifacts: 'artefacts',
  };
</script>

<div class="grid">
  <div class="field">
    <label for="{id}-name">Nom</label>
    <input
      id="{id}-name"
      type="text"
      class="mono"
      bind:value={action.name}
      class:bad={bad(`${p}.name`)}
      data-path="{p}.name"
      placeholder="identify_impacts"
    />
  </div>
  <div class="field">
    <label for="{id}-kind">Type</label>
    <select id="{id}-kind" bind:value={action.kind} class:bad={bad(`${p}.kind`)} data-path="{p}.kind">
      {#each ACTION_KINDS as k (k)}<option value={k}>{KIND_LABELS[k]}</option>{/each}
    </select>
  </div>
  <div class="field">
    <label for="{id}-cost">Coût</label>
    <input
      id="{id}-cost"
      type="number"
      min="0"
      step="any"
      bind:value={action.cost}
      class:bad={bad(`${p}.cost`)}
      data-path="{p}.cost"
    />
  </div>
  <div class="field">
    <label for="{id}-perm">Permission <span class="opt">(facultative)</span></label>
    <input
      id="{id}-perm"
      type="text"
      class="mono"
      bind:value={action.permission}
      class:bad={bad(`${p}.permission`)}
      data-path="{p}.permission"
      placeholder="change:apply"
    />
  </div>
</div>

<div class="field">
  <label for="{id}-desc">Description</label>
  <input
    id="{id}-desc"
    type="text"
    bind:value={action.description}
    class:bad={bad(`${p}.description`)}
    data-path="{p}.description"
  />
</div>

<div class="grid2 field">
  <CondRows bind:rows={action.pre} options={conditions} path="{p}.pre" label="Préconditions" {bad} {readonly} />
  <CondRows bind:rows={action.effects} options={conditions} path="{p}.effects" label="Effets" {bad} {readonly} />
</div>

{#if action.kind === 'llm'}
  <div class="field">
    <label for="{id}-model">Modèle</label>
    <input
      id="{id}-model"
      type="text"
      bind:value={action.model}
      class:bad={bad(`${p}.model`)}
      data-path="{p}.model"
      placeholder="default"
    />
  </div>
  <div class="field">
    <label for="{id}-prompt">Prompt <span class="opt">(gabarit Go : {'{{ .Change.Intent }}'}…)</span></label>
    <textarea
      id="{id}-prompt"
      class="mono"
      rows="8"
      bind:value={action.prompt}
      class:bad={bad(`${p}.prompt`)}
      data-path="{p}.prompt"
    ></textarea>
  </div>
{:else if action.kind === 'tool'}
  <div class="field">
    <label for="{id}-tool">Outil</label>
    <input
      id="{id}-tool"
      type="text"
      class="mono"
      bind:value={action.tool}
      class:bad={bad(`${p}.tool`)}
      data-path="{p}.tool"
    />
  </div>
{:else if action.kind === 'human'}
  <div class="field">
    <label for="{id}-instr">Instructions</label>
    <textarea
      id="{id}-instr"
      rows="3"
      bind:value={action.instructions}
      class:bad={bad(`${p}.instructions`)}
      data-path="{p}.instructions"
    ></textarea>
  </div>
{:else if action.kind === 'builtin'}
  <div class="grid2">
    <div class="field">
      <label for="{id}-builtin">Fonction intégrée</label>
      <input
        id="{id}-builtin"
        type="text"
        class="mono"
        bind:value={action.builtin}
        class:bad={bad(`${p}.builtin`)}
        data-path="{p}.builtin"
        placeholder="graph.propagate"
      />
    </div>
    <div class="field">
      <label for="{id}-params">Paramètres <span class="opt">(objet JSON)</span></label>
      <textarea
        id="{id}-params"
        class="mono"
        rows="4"
        bind:value={action.params}
        class:bad={bad(`${p}.params`)}
        data-path="{p}.params"
        placeholder={'{ "maxDepth": 3 }'}
      ></textarea>
    </div>
  </div>
{/if}

<div class="expects" class:on={action.hasExpects} data-path="{p}.expects">
  <label class="check">
    <input type="checkbox" bind:checked={action.hasExpects} />
    Résultats attendus <span class="opt">(expects — génère la condition <code>expect:{action.name || '…'}</code>)</span>
  </label>
  {#if action.hasExpects}
    <div class="grid">
      <div class="field">
        <label for="{id}-fe">Pour chaque</label>
        <select
          id="{id}-fe"
          bind:value={action.expects.forEach}
          class:bad={bad(`${p}.expects.forEach`)}
          data-path="{p}.expects.forEach"
        >
          {#each FOR_EACH as f (f)}<option value={f}>{FOR_EACH_LABELS[f]}</option>{/each}
        </select>
      </div>
      <div class="field">
        <label for="{id}-where">Filtre <span class="opt">(CEL, variable <code>x</code>)</span></label>
        <input
          id="{id}-where"
          type="text"
          class="mono"
          bind:value={action.expects.where}
          class:bad={bad(`${p}.expects.where`)}
          data-path="{p}.expects.where"
          placeholder={'x.target.type == "Requirement"'}
        />
      </div>
      <div class="field">
        <label for="{id}-op">Produire</label>
        <select
          id="{id}-op"
          bind:value={action.expects.op}
          class:bad={bad(`${p}.expects.produce`)}
          data-path="{p}.expects.produce"
        >
          <option value="">— rien —</option>
          {#each PRODUCE_OPS as o (o)}<option value={o}>{o}</option>{/each}
        </select>
      </div>
      {#if action.expects.op}
        <div class="field">
          <label for="{id}-nt">Type de nœud</label>
          <select
            id="{id}-nt"
            bind:value={action.expects.nodeType}
            class:bad={bad(`${p}.expects.produce.nodeType`)}
            data-path="{p}.expects.produce.nodeType"
          >
            <option value="">— aucun —</option>
            {#if action.expects.nodeType && !nodeTypes.includes(action.expects.nodeType)}
              <option value={action.expects.nodeType}>{action.expects.nodeType} (inconnu)</option>
            {/if}
            {#each nodeTypes as n (n)}<option value={n}>{n}</option>{/each}
          </select>
        </div>
      {/if}
      <div class="field">
        <label for="{id}-lt">Lien</label>
        <select
          id="{id}-lt"
          bind:value={action.expects.linkType}
          class:bad={bad(`${p}.expects.link`)}
          data-path="{p}.expects.link"
        >
          <option value="">— aucun —</option>
          {#if action.expects.linkType && !linkTypes.includes(action.expects.linkType)}
            <option value={action.expects.linkType}>{action.expects.linkType} (inconnu)</option>
          {/if}
          {#each linkTypes as l (l)}<option value={l}>{l}</option>{/each}
        </select>
      </div>
      {#if action.expects.linkType}
        <div class="field">
          <label for="{id}-dir">Sens du lien</label>
          <select
            id="{id}-dir"
            bind:value={action.expects.direction}
            class:bad={bad(`${p}.expects.link.direction`)}
            data-path="{p}.expects.link.direction"
          >
            <option value="out">out — du produit vers l'élément</option>
            <option value="in">in — de l'élément vers le produit</option>
          </select>
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
    gap: 0 0.85rem;
  }
  .grid2 {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 0.6rem 0.85rem;
  }
  .opt {
    font-weight: 400;
  }
  .expects {
    border-top: 1px solid var(--border);
    padding-top: 0.6rem;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    margin-bottom: 0.5rem;
    color: var(--text);
  }
  .check input {
    margin: 0;
  }
</style>
