<script lang="ts">
  // Édition d'une action de la méthodologie.
  import { ACTION_KINDS, FOR_EACH, PRODUCE_OPS, SCRIPT_LANGUAGES, type ActionForm } from '../methodologyForm';
  import { TEMPLATES } from '../dsl';
  import CondRows from './CondRows.svelte';
  import CodeEditor from './CodeEditor.svelte';

  let {
    action = $bindable(),
    index,
    conditions,
    nodeTypes,
    linkTypes,
    actions = [],
    bad,
    readonly = false,
    onrename,
  }: {
    action: ActionForm;
    index: number;
    conditions: string[];
    nodeTypes: string[];
    linkTypes: string[];
    /** noms des autres actions (cibles possibles d'une spécialisation) */
    actions?: string[];
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
    /** renommage validé (perte du focus) : mise à jour des références */
    onrename?: (from: string, to: string) => void;
  } = $props();

  let nameAtFocus = '';

  function setLanguage(lang: string) {
    // Le modèle d'un langage est remplacé par celui de l'autre.
    const isTemplate = !action.code.trim() || Object.values(TEMPLATES).includes(action.code);
    action.language = lang;
    if (isTemplate && action.code.trim()) action.code = TEMPLATES[lang] ?? '';
  }

  const LANGUAGE_LABELS: Record<string, string> = { javascript: 'JavaScript (goja)', go: 'Go (yaegi)' };

  const p = $derived(`actions[${index}]`);
  /** une spécialisation n'est pas planifiée : pré / effets / attendus / coût hérités */
  const isSpec = $derived(!!action.specializes.trim());
  const specTargets = $derived(actions.filter((a) => a && a !== action.name));
  const id = $derived(`act-${index}`);

  const KIND_LABELS: Record<string, string> = {
    llm: 'llm — modèle de langage',
    script: 'script — code JavaScript / Go',
    tool: 'tool — outil externe',
    human: 'human — tâche humaine',
    builtin: 'builtin — fonction intégrée',
    abstract: 'abstract — sans implémentation (à spécialiser)',
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
      onfocus={() => (nameAtFocus = action.name)}
      onchange={() => {
        if (nameAtFocus && nameAtFocus !== action.name) onrename?.(nameAtFocus, action.name);
        nameAtFocus = action.name;
      }}
    />
  </div>
  <div class="field">
    <label for="{id}-kind">Type</label>
    <select id="{id}-kind" bind:value={action.kind} class:bad={bad(`${p}.kind`)} data-path="{p}.kind">
      {#each ACTION_KINDS as k (k)}<option value={k}>{KIND_LABELS[k]}</option>{/each}
    </select>
  </div>
  {#if !isSpec}
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
      <label class="check" title="Les effets sont atteints en plusieurs exécutions (ex. un build par technologie) : une exécution qui produit des items sans atteindre les effets est un progrès, pas un échec.">
        <input type="checkbox" bind:checked={action.incremental} />
        Incrémentale
      </label>
    </div>
  {/if}
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

<div class="field">
  <label for="{id}-utility">Utilité <span class="opt">(expression CEL numérique — planificateurs utility / hybrid)</span></label>
  <CodeEditor
    id="{id}-utility"
    bind:value={action.utility}
    language="cel"
    lineNumbers={false}
    {readonly}
    label="Utilité"
    minHeight="1.9rem"
    maxHeight="8rem"
    placeholder="ex. size(impacts) * 2.0"
    bad={bad(`${p}.utility`)}
    path="{p}.utility"
  />
</div>

<div class="spec" class:on={isSpec} data-path="{p}.specializes">
  <div class="grid">
    <div class="field">
      <label for="{id}-spec">Spécialise <span class="opt">(facultatif)</span></label>
      <input
        id="{id}-spec"
        type="text"
        class="mono"
        list="{id}-spec-list"
        bind:value={action.specializes}
        class:bad={bad(`${p}.specializes`)}
        data-path="{p}.specializes"
        placeholder="action ou méthodologie/action"
      />
      <datalist id="{id}-spec-list">
        {#each specTargets as a (a)}<option value={a}></option>{/each}
      </datalist>
    </div>
    {#if isSpec}
      <div class="field">
        <label for="{id}-prio">Priorité</label>
        <input
          id="{id}-prio"
          type="number"
          step="1"
          bind:value={action.priority}
          class:bad={bad(`${p}.priority`)}
          data-path="{p}.priority"
        />
      </div>
    {/if}
  </div>
  {#if isSpec}
    <div class="field">
      <label for="{id}-when">Garde <span class="opt">(when — expression CEL sur le tableau noir ; vide : toujours)</span></label>
      <CodeEditor
        id="{id}-when"
        bind:value={action.when}
        language="cel"
        lineNumbers={false}
        {readonly}
        label="Garde de la spécialisation"
        minHeight="1.9rem"
        maxHeight="8rem"
        placeholder={'ex. size(impacts) > 10'}
        bad={bad(`${p}.when`)}
        path="{p}.when"
      />
    </div>
    <p class="hint">
      Une spécialisation n'est pas planifiée : elle hérite des préconditions, effets, résultats attendus et du coût de
      l'action <code>{action.specializes}</code> et remplace son implémentation à l'exécution quand sa garde est vraie. Si
      plusieurs spécialisations s'appliquent, la priorité la plus haute l'emporte.
    </p>
  {:else}
    <p class="hint">
      Renseignez une action (<code>action</code>, ou <code>méthodologie/action</code> pour une autre méthodologie) pour faire
      de celle-ci une spécialisation qui la remplace à l'exécution sous condition.
    </p>
  {/if}
</div>

{#if action.kind === 'abstract'}
  <p class="hint abstract">
    Action abstraite : planifiée comme les autres mais sans implémentation. Elle doit être spécialisée (au moins une
    spécialisation applicable) pour pouvoir s'exécuter.
  </p>
{/if}

{#if !isSpec}
  <div class="grid2 field">
    <CondRows bind:rows={action.pre} options={conditions} path="{p}.pre" label="Préconditions" {bad} {readonly} />
    <CondRows bind:rows={action.effects} options={conditions} path="{p}.effects" label="Effets" {bad} {readonly} />
  </div>
{/if}

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
    <CodeEditor
      id="{id}-prompt"
      bind:value={action.prompt}
      language="text"
      wrap
      {readonly}
      label="Prompt"
      minHeight="10rem"
      bad={bad(`${p}.prompt`)}
      path="{p}.prompt"
    />
  </div>
{:else if action.kind === 'script'}
  <div class="script-head">
    <div class="field lang">
      <label for="{id}-lang">Langage</label>
      <select
        id="{id}-lang"
        value={action.language}
        onchange={(e) => setLanguage(e.currentTarget.value)}
        class:bad={bad(`${p}.language`)}
        data-path="{p}.language"
      >
        {#each SCRIPT_LANGUAGES as l (l)}<option value={l}>{LANGUAGE_LABELS[l]}</option>{/each}
      </select>
    </div>
    <p class="hint grow">
      Le code s'exécute dans le sandbox du processus avec l'objet <code>ctx</code> (<kbd>Ctrl</kbd>+<kbd>Espace</kbd> pour
      la complétion, voir « Aide DSL »). Les écritures sont appliquées de façon atomique à la fin de l'action.
    </p>
    {#if !readonly && !action.code.trim()}
      <button type="button" class="small" onclick={() => (action.code = TEMPLATES[action.language] ?? '')}>Insérer un modèle</button>
    {/if}
  </div>
  <div class="field">
    <label for="{id}-code">Code</label>
    <CodeEditor
      id="{id}-code"
      bind:value={action.code}
      language={action.language === 'go' ? 'go' : 'javascript'}
      dsl
      {readonly}
      label="Code de l'action"
      minHeight="16rem"
      maxHeight="60vh"
      bad={bad(`${p}.code`)}
      path="{p}.code"
    />
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

{#if !isSpec}
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
{/if}

<style>
  .script-head {
    display: flex;
    gap: 0.8rem;
    align-items: flex-end;
    flex-wrap: wrap;
  }
  .script-head .lang {
    width: 200px;
  }
  .script-head .hint {
    margin-bottom: 0.7rem;
    min-width: 240px;
  }
  .script-head button {
    margin-bottom: 0.7rem;
  }
  kbd {
    font-family: var(--mono);
    font-size: 0.85em;
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 3px;
    background: var(--surface-2);
  }
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
  .spec {
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    padding-top: 0.6rem;
    margin-bottom: 0.7rem;
  }
  .spec .hint {
    margin: 0 0 0.6rem;
  }
  .spec.on {
    box-shadow: inset 3px 0 0 var(--info);
    padding-left: 0.6rem;
  }
  .hint.abstract {
    margin: 0 0 0.7rem;
    color: var(--info);
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
