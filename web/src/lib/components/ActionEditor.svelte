<script lang="ts">
  // Editing a methodology action.
  import { ACTION_KINDS, FOR_EACH, PRODUCE_OPS, SCRIPT_LANGUAGES, type ActionForm } from '../methodologyForm';
  import { TEMPLATES } from '../dsl';
  import CondRows from './CondRows.svelte';
  import CodeEditor from './CodeEditor.svelte';
  import { modelChoices, refreshModelChoices, isAvailableModel } from '../stores/modelChoices.svelte';

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
    /** names of other actions (possible targets of a specialization) */
    actions?: string[];
    bad: (path: string, exact?: boolean) => boolean;
    readonly?: boolean;
    /** confirmed rename (on blur): updates references */
    onrename?: (from: string, to: string) => void;
  } = $props();

  $effect(() => {
    if (action.kind === 'llm' && !modelChoices.loaded) void refreshModelChoices();
  });

  const defaultAlias = $derived(modelChoices.aliases.find((a) => a.alias === 'default'));

  let nameAtFocus = '';

  function setLanguage(lang: string) {
    // The template for a language is replaced by the other one's.
    const isTemplate = !action.code.trim() || Object.values(TEMPLATES).includes(action.code);
    action.language = lang;
    if (isTemplate && action.code.trim()) action.code = TEMPLATES[lang] ?? '';
  }

  const LANGUAGE_LABELS: Record<string, string> = { javascript: 'JavaScript (goja)', go: 'Go (yaegi)' };

  const p = $derived(`actions[${index}]`);
  /** a specialization is not planned: pre / effects / expects / cost are inherited */
  const isSpec = $derived(!!action.specializes.trim());
  const specTargets = $derived(actions.filter((a) => a && a !== action.name));
  const id = $derived(`act-${index}`);

  const KIND_LABELS: Record<string, string> = {
    llm: 'llm — language model',
    script: 'script — JavaScript / Go code',
    tool: 'tool — external tool',
    human: 'human — human task',
    builtin: 'builtin — built-in function',
    abstract: 'abstract — no implementation (to be specialized)',
  };
  const FOR_EACH_LABELS: Record<string, string> = {
    impacts: 'impacts',
    proposals: 'proposals',
    items: 'items',
    artifacts: 'artifacts',
  };
</script>

<div class="grid">
  <div class="field">
    <label for="{id}-name">Name</label>
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
    <label for="{id}-kind">Kind</label>
    <select id="{id}-kind" bind:value={action.kind} class:bad={bad(`${p}.kind`)} data-path="{p}.kind">
      {#each ACTION_KINDS as k (k)}<option value={k}>{KIND_LABELS[k]}</option>{/each}
    </select>
  </div>
  {#if !isSpec}
    <div class="field">
      <label for="{id}-cost">Cost</label>
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
      <label class="check" title="The effects are reached over several runs (e.g. one build per technology): a run that produces items without reaching the effects is progress, not a failure.">
        <input type="checkbox" bind:checked={action.incremental} />
        Incremental
      </label>
    </div>
  {/if}
  <div class="field">
    <label for="{id}-perm">Permission <span class="opt">(optional)</span></label>
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
  <label for="{id}-utility">Utility <span class="opt">(numeric CEL expression — utility / hybrid planners)</span></label>
  <CodeEditor
    id="{id}-utility"
    bind:value={action.utility}
    language="cel"
    lineNumbers={false}
    {readonly}
    label="Utility"
    minHeight="1.9rem"
    maxHeight="8rem"
    placeholder="e.g. size(impacts) * 2.0"
    bad={bad(`${p}.utility`)}
    path="{p}.utility"
  />
</div>

<div class="spec" class:on={isSpec} data-path="{p}.specializes">
  <div class="grid">
    <div class="field">
      <label for="{id}-spec">Specializes <span class="opt">(optional)</span></label>
      <input
        id="{id}-spec"
        type="text"
        class="mono"
        list="{id}-spec-list"
        bind:value={action.specializes}
        class:bad={bad(`${p}.specializes`)}
        data-path="{p}.specializes"
        placeholder="action or methodology/action"
      />
      <datalist id="{id}-spec-list">
        {#each specTargets as a (a)}<option value={a}></option>{/each}
      </datalist>
    </div>
    {#if isSpec}
      <div class="field">
        <label for="{id}-prio">Priority</label>
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
      <label for="{id}-when">Guard <span class="opt">(when — CEL expression over the blackboard; empty: always)</span></label>
      <CodeEditor
        id="{id}-when"
        bind:value={action.when}
        language="cel"
        lineNumbers={false}
        {readonly}
        label="Specialization guard"
        minHeight="1.9rem"
        maxHeight="8rem"
        placeholder={'e.g. size(impacts) > 10'}
        bad={bad(`${p}.when`)}
        path="{p}.when"
      />
    </div>
    <p class="hint">
      A specialization is not planned: it inherits the preconditions, effects, expected results and cost of
      action <code>{action.specializes}</code> and replaces its implementation at run time when its guard is true. If
      several specializations apply, the highest priority wins.
    </p>
  {:else}
    <p class="hint">
      Fill in an action (<code>action</code>, or <code>methodology/action</code> for another methodology) to make
      this one a specialization that replaces it at run time under a condition.
    </p>
  {/if}
</div>

{#if action.kind === 'abstract'}
  <p class="hint abstract">
    Abstract action: planned like any other but with no implementation. It must be specialized (at least one
    applicable specialization) before it can run.
  </p>
{/if}

{#if !isSpec}
  <div class="grid2 field">
    <CondRows bind:rows={action.pre} options={conditions} path="{p}.pre" label="Preconditions" {bad} {readonly} />
    <CondRows bind:rows={action.effects} options={conditions} path="{p}.effects" label="Effects" {bad} {readonly} />
  </div>
{/if}

{#if action.kind === 'llm'}
  <div class="field">
    <label for="{id}-model">Model</label>
    <select
      id="{id}-model"
      bind:value={action.model}
      class:bad={bad(`${p}.model`) || (modelChoices.loaded && !!action.model.trim() && !isAvailableModel(action.model))}
      data-path="{p}.model"
      disabled={readonly}
    >
      <option value="">default{defaultAlias ? ` — ${defaultAlias.provider}/${defaultAlias.model}` : ''}</option>
      {#if action.model.trim() && modelChoices.loaded && !isAvailableModel(action.model)}
        <option value={action.model}>{action.model} (not available to you)</option>
      {/if}
      {#if modelChoices.aliases.some((a) => a.alias !== 'default')}
        <optgroup label="Aliases">
          {#each modelChoices.aliases.filter((a) => a.alias !== 'default') as a (a.alias)}
            <option value={a.alias}>{a.alias} — {a.provider}/{a.model}</option>
          {/each}
        </optgroup>
      {/if}
      <optgroup label="Models">
        {#each modelChoices.models as m (m.provider + '/' + m.model)}
          <option value={`${m.provider}/${m.model}`}>{m.provider}/{m.displayName || m.model}</option>
        {/each}
      </optgroup>
    </select>
    {#if modelChoices.loaded && !!action.model.trim() && !isAvailableModel(action.model)}
      <span class="hint">This model is not in your list of authorized models (removed, disabled, or restricted by role). Pick another one.</span>
    {:else if modelChoices.error}
      <span class="hint">Model list unavailable: {modelChoices.error}</span>
    {:else}
      <span class="hint">Only models enabled and authorized for you in Platform settings are listed.</span>
    {/if}
  </div>
  <div class="field">
    <label for="{id}-prompt">Prompt <span class="opt">(Go template: {'{{ .Change.Intent }}'}…)</span></label>
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
  <div class="field">
    <label for="{id}-mcps">MCPs used</label>
    <input
      id="{id}-mcps"
      type="text"
      class="mono"
      bind:value={action.mcps}
      class:bad={bad(`${p}.mcps`)}
      data-path="{p}.mcps"
      placeholder="document-repository"
      disabled={readonly}
    />
    <span class="hint"
      >Comma separated. The action can call the tools of these MCPs only, and is scheduled only in a change whose organisation binds them all.</span
    >
  </div>
{:else if action.kind === 'script'}
  <div class="script-head">
    <div class="field lang">
      <label for="{id}-lang">Language</label>
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
      The code runs in the process sandbox with the <code>ctx</code> object (<kbd>Ctrl</kbd>+<kbd>Space</kbd> for
      completion, see "DSL Help"). Writes are applied atomically at the end of the action.
    </p>
    {#if !readonly && !action.code.trim()}
      <button type="button" class="small" onclick={() => (action.code = TEMPLATES[action.language] ?? '')}>Insert a template</button>
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
      label="Action code"
      minHeight="16rem"
      maxHeight="60vh"
      bad={bad(`${p}.code`)}
      path="{p}.code"
    />
  </div>
  <div class="field">
    <label for="{id}-mcps">MCPs used</label>
    <input
      id="{id}-mcps"
      type="text"
      class="mono"
      bind:value={action.mcps}
      class:bad={bad(`${p}.mcps`)}
      data-path="{p}.mcps"
      placeholder="document-repository"
      disabled={readonly}
    />
    <span class="hint"
      >Comma separated. The action can call the tools of these MCPs only, and is scheduled only in a change whose organisation binds them all.</span
    >
  </div>
{:else if action.kind === 'tool'}
  <div class="field">
    <label for="{id}-tool">Tool</label>
    <input
      id="{id}-tool"
      type="text"
      class="mono"
      bind:value={action.tool}
      class:bad={bad(`${p}.tool`)}
      data-path="{p}.tool"
      placeholder="document-repository/read"
    />
    <span class="hint">Qualified name <code>&lt;mcp&gt;/&lt;tool&gt;</code>. The action needs the MCP bound by the organisation of the change.</span>
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
      <label for="{id}-builtin">Built-in function</label>
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
      <label for="{id}-params">Parameters <span class="opt">(JSON object)</span></label>
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
    Expected results <span class="opt">(expects — generates the condition <code>expect:{action.name || '…'}</code>)</span>
  </label>
  {#if action.hasExpects}
    <div class="grid">
      <div class="field">
        <label for="{id}-fe">For each</label>
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
        <label for="{id}-where">Filter <span class="opt">(CEL, variable <code>x</code>)</span></label>
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
        <label for="{id}-op">Produce</label>
        <select
          id="{id}-op"
          bind:value={action.expects.op}
          class:bad={bad(`${p}.expects.produce`)}
          data-path="{p}.expects.produce"
        >
          <option value="">— nothing —</option>
          {#each PRODUCE_OPS as o (o)}<option value={o}>{o}</option>{/each}
        </select>
      </div>
      {#if action.expects.op}
        <div class="field">
          <label for="{id}-nt">Node type</label>
          <select
            id="{id}-nt"
            bind:value={action.expects.nodeType}
            class:bad={bad(`${p}.expects.produce.nodeType`)}
            data-path="{p}.expects.produce.nodeType"
          >
            <option value="">— none —</option>
            {#if action.expects.nodeType && !nodeTypes.includes(action.expects.nodeType)}
              <option value={action.expects.nodeType}>{action.expects.nodeType} (unknown)</option>
            {/if}
            {#each nodeTypes as n (n)}<option value={n}>{n}</option>{/each}
          </select>
        </div>
      {/if}
      <div class="field">
        <label for="{id}-lt">Link</label>
        <select
          id="{id}-lt"
          bind:value={action.expects.linkType}
          class:bad={bad(`${p}.expects.link`)}
          data-path="{p}.expects.link"
        >
          <option value="">— none —</option>
          {#if action.expects.linkType && !linkTypes.includes(action.expects.linkType)}
            <option value={action.expects.linkType}>{action.expects.linkType} (unknown)</option>
          {/if}
          {#each linkTypes as l (l)}<option value={l}>{l}</option>{/each}
        </select>
      </div>
      {#if action.expects.linkType}
        <div class="field">
          <label for="{id}-dir">Link direction</label>
          <select
            id="{id}-dir"
            bind:value={action.expects.direction}
            class:bad={bad(`${p}.expects.link.direction`)}
            data-path="{p}.expects.link.direction"
          >
            <option value="out">out — from the product to the element</option>
            <option value="in">in — from the element to the product</option>
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
