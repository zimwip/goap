<script lang="ts">
  // Single global "New object" dialog, mounted once at the shell root.
  import { objectDialog, closeObjectDialog } from './objectDialogState.svelte';
  import { notify } from './workbench.svelte';
  import { graph, errorMessage, nodeTitle, type GraphNode, type Struct } from '../api';
  import { createNodeItem } from '../graphEdit';
  import { MAIN_BRANCH } from '../namespace';
  import { typeCatalog } from '../stores/types.svelte';
  import FormRenderer from '../forms/FormRenderer.svelte';
  import type { FormValues, NodeOption, NodeSource } from '../forms/types';
  import { nodeWrite } from '../forms/nodeForm';
  import { committableBirthStates } from '../lifecycle';

  let key = $state('');
  let values = $state<Record<string, string>>({});
  let busy = $state(false);
  let error = $state('');
  let keyInput = $state<HTMLInputElement>();

  $effect(() => {
    if (!objectDialog.open) return;
    key = '';
    values = {};
    error = '';
    busy = false;
    queueMicrotask(() => keyInput?.focus());
  });

  // the dialog applies its change on main at once, so the node is born in a state it can commit in (ADR 0078)
  const lifecycle = $derived(objectDialog.open ? typeCatalog.cat.lifecycle(objectDialog.nodeType) : undefined);
  const states = $derived(committableBirthStates(lifecycle));
  let birth = $state('');
  $effect(() => {
    birth = states[0] ?? '';
  });

  const namespaceOf = (type: string) => (type.includes('@') ? type.slice(0, type.indexOf('@')) : '');
  // the creation form of the type (ADR 0115 §10), its link fields searching the nodes at the head of the namespace
  const typeForm = $derived(objectDialog.open ? typeCatalog.cat.type(objectDialog.nodeType)?.form : undefined);
  /** the head of the namespace, its header only: the pickers search it, never read it whole */
  const headOf = async (namespace: string) => (await graph.getBranch(namespace, MAIN_BRANCH)).head?.id ?? '';
  let headId = $state('');
  /** the nodes picked so far, kept so a choice stays shown once the search moved on */
  let picked = $state<NodeOption[]>([]);
  $effect(() => {
    headId = '';
    picked = [];
    if (!typeForm) return;
    let gone = false;
    headOf(namespaceOf(objectDialog.nodeType))
      .then((h) => !gone && (headId = h))
      .catch(() => undefined); // a namespace with no baseline yet: nothing to link to
    return () => (gone = true);
  });
  const optionOf = (n: GraphNode): NodeOption => ({ key: n.key ?? '', type: n.type ?? '', title: nodeTitle(n), state: n.state, namespace: n.namespace });
  const formSource = $derived.by((): NodeSource => {
    const cat = typeCatalog.cat;
    const t = objectDialog.nodeType;
    const b = headId;
    return {
      nodes: picked,
      search: async (query) =>
        b ? ((await graph.listBaselineNodes({ view: { namespace: namespaceOf(t), branch: MAIN_BRANCH, baselineId: b }, query, limit: 200 })).nodes ?? []).filter((n) => n.key && !n.deleted).map(optionOf) : [],
      remember: (n) => {
        if (!picked.some((p) => p.key === n.key)) picked = [...picked, n];
      },
      isA: (x, a) => x === a || cat.typesOf(x).includes(a),
      linkTypesFrom: () => cat.linksFrom(t).map((l) => l.ref ?? '').filter(Boolean),
      linkLabel: (l) => cat.links.find((x) => x.ref === l)?.label ?? '',
    };
  });

  async function createFromForm(v: FormValues) {
    if (!typeForm) return;
    const w = nodeWrite(typeForm, v);
    await submit(w.props as Struct, w.links.map((l) => ({ type: l.type, toKey: l.to })));
  }

  function create(e: SubmitEvent) {
    e.preventDefault();
    if (typeForm) return;
    const props: Struct = {};
    for (const [k, v] of Object.entries(values)) if (v.trim()) props[k] = v.trim();
    void submit(props, []);
  }

  async function submit(props: Struct, links: { type: string; toKey: string }[]) {
    if (!key.trim()) {
      error = 'The key is required.';
      return;
    }
    busy = true;
    error = '';
    try {
      // every node is created in a change (ADR 0076): one of the namespace of its type, applied on main
      const type = objectDialog.nodeType;
      const namespace = namespaceOf(type);
      const baselineId = headId || (await headOf(namespace));
      await graph.commitEdits({
        namespace,
        title: `Create ${key.trim()}`,
        intent: `Create the ${type} ${key.trim()}`,
        baselineId,
        methodology: objectDialog.methodology,
        edits: [{ ...createNodeItem(key.trim(), type, props, links), ...(birth && birth !== lifecycle?.initial ? { state: birth } : {}) }],
      });
      notify(`${objectDialog.nodeType} ${key.trim()} created.`, 'ok');
      closeObjectDialog();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:window onkeydown={(e) => objectDialog.open && e.key === 'Escape' && closeObjectDialog()} />

{#if objectDialog.open}
  <div class="backdrop" role="presentation" onmousedown={closeObjectDialog}>
    <div class="dialog" role="dialog" aria-label="New object" tabindex="-1" onmousedown={(e) => e.stopPropagation()}>
    <form onsubmit={create}>
      <h3>New {objectDialog.nodeType}</h3>
      <div class="field">
        <label for="obj-key">Key</label>
        <input id="obj-key" type="text" class="mono" bind:value={key} bind:this={keyInput} placeholder="REQ-10" />
      </div>
      {#if states.length > 1 || (lifecycle && states[0] !== lifecycle.initial)}
        <div class="field">
          <label for="obj-state">State</label>
          <select id="obj-state" bind:value={birth}>
            {#each states as s (s)}<option value={s}>{s}</option>{/each}
          </select>
        </div>
      {/if}
      {#if !typeForm}
        {#each objectDialog.properties as p (p)}
          <div class="field">
            <label for="obj-{p}">{p}</label>
            <input id="obj-{p}" type="text" bind:value={values[p]} />
          </div>
        {/each}
        {#if error}<div class="alert small">{error}</div>{/if}
        <div class="row">
          <button type="button" class="ghost" onclick={closeObjectDialog}>Cancel</button>
          <button type="submit" class="primary" disabled={busy}>{busy ? 'Creating…' : 'Create'}</button>
        </div>
      {/if}
    </form>
    {#if typeForm}
      {#key objectDialog.nodeType}
        <FormRenderer form={typeForm} source={formSource} {busy} serverError={error} submitLabel="Create" onsubmit={(v) => void createFromForm(v)}
          assist={{ id: `create:${objectDialog.nodeType}`, label: `New ${objectDialog.nodeType}; the key is asked above the form` }}>
          {#snippet actions()}<button type="button" class="ghost" onclick={closeObjectDialog}>Cancel</button>{/snippet}
        </FormRenderer>
      {/key}
    {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 95;
    display: grid;
    place-items: center;
    background: rgba(0, 0, 0, 0.4);
  }
  .dialog {
    width: min(420px, calc(100vw - 28px));
    max-height: calc(100vh - 40px);
    overflow: auto;
    display: grid;
    gap: 0.6rem;
    padding: 1rem;
    background: var(--surface);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow-pop);
  }
  form {
    display: grid;
    gap: 0.6rem;
  }
  h3 {
    margin: 0;
  }
  .row {
    display: flex;
    justify-content: flex-end;
    gap: 0.4rem;
  }
</style>
