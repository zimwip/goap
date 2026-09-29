<script lang="ts">
  // Branches of a namespace (ADR 0009, ADR 0032): where each forked from, its head, its status. A branch is opened
  // from a baseline, merged into another (a node changed on one side joins as is, a node changed on both sides is
  // resolved here) and closed. Changes land on a branch (New change, "Lands on"); their own branches (change-…) and
  // the branches of their flows and options (flow-…) are listed apart, they are driven from the change.
  import { untrack } from 'svelte';
  import { graph, errorMessage, formatDate, shortId, type Baseline, type Branch, type Resolution } from '../../api';
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import MergeResolver from '../../components/MergeResolver.svelte';
  import { openTab } from '../../shell/tabs.svelte';
  import { notify, provideActions } from '../../shell/workbench.svelte';
  import { refreshBaselines } from '../../stores/catalog.svelte';

  let { tab }: { tab: Tab } = $props();
  const namespace = $derived(tab.params.namespace ?? '');

  let branches = $state<Branch[]>([]);
  let baselines = $state<Baseline[]>([]);
  let error = $state('');
  let busy = $state('');
  let showInternal = $state(false);
  // new branch
  let name = $state('');
  let from = $state('');
  // merge
  let mergeFrom = $state(untrack(() => tab.params.from) ?? '');
  let mergeInto = $state('main');

  async function load() {
    if (!namespace) return;
    try {
      const [br, bs] = await Promise.all([graph.listBranches(namespace), graph.listBaselines(namespace)]);
      branches = br.branches ?? [];
      if (!branches.some((b) => b.name === 'main')) branches = [{ name: 'main', namespace, status: 'open' }, ...branches];
      baselines = (bs.baselines ?? []).slice().reverse();
      if (!from) from = baselines[0]?.id ?? '';
      error = '';
    } catch (e) {
      error = errorMessage(e);
    }
  }
  $effect(() => {
    void namespace;
    void load();
  });

  const internal = (b: Branch) => !!b.name && (b.name.startsWith('change-') || b.name.startsWith('flow-'));
  const shown = $derived(branches.filter((b) => showInternal || !internal(b)));
  const open = $derived(branches.filter((b) => b.status === 'open'));
  const baselineName = (id: string | undefined) => (id ? (baselines.find((b) => b.id === id)?.name || shortId(id)) : '—');
  const openBaseline = (id: string | undefined) => id && openTab({ kind: 'baseline', params: { id, name: baselineName(id) } }, { pin: true });

  async function act(label: string, fn: () => Promise<unknown>, done: string) {
    busy = label;
    error = '';
    try {
      await fn();
      await load();
      void refreshBaselines(namespace);
      notify(done, 'ok');
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = '';
    }
  }

  const create = () =>
    act('create', () => graph.createBranch({ namespace, name: name.trim(), fromBaseline: from }), `Branch ${name.trim()} opened.`).then(() => (name = ''));

  async function merge(resolutions: Record<string, Resolution>): Promise<boolean> {
    const r = await graph.mergeBranch({ namespace, from: mergeFrom, into: mergeInto, resolutions });
    await load();
    void refreshBaselines(namespace);
    notify(`${mergeFrom} merged into ${mergeInto}: baseline ${r.baseline?.name || shortId(r.baseline?.id)}.`, 'ok');
    return true;
  }

  provideActions(
    () => tab.id,
    () => [{ id: 'refresh', label: 'Refresh', icon: 'refresh', run: load }],
  );
</script>

<div class="editor-page">
  <div class="editor-head">
    <Icon name="branch" size={18} />
    <h2>Branches of {namespace}</h2>
  </div>
  {#if error}<div class="alert">{error}</div>{/if}

  <section class="card">
    <div class="head">
      <h3>Branches <span class="count">{shown.length}</span></h3>
      <span class="grow"></span>
      <label class="check"><input type="checkbox" bind:checked={showInternal} /> change and option branches</label>
    </div>
    <table>
      <thead><tr><th>Branch</th><th>Forked from</th><th>Head</th><th>Status</th><th></th></tr></thead>
      <tbody>
        {#each shown as b (b.name)}
          <tr>
            <td><code>{b.name}</code>{#if b.origin}<span class="hint"> · {b.origin}</span>{/if}</td>
            <td>{#if b.parent}<code>{b.parent}</code> at <button type="button" class="link" onclick={() => openBaseline(b.forkBaseline)}>{baselineName(b.forkBaseline)}</button>{:else}<span class="hint">—</span>{/if}</td>
            <td>{#if b.head}<button type="button" class="link" onclick={() => openBaseline(b.head)}>{baselineName(b.head)}</button>{:else}<span class="hint">—</span>{/if}</td>
            <td><StatusBadge status={b.status} />{#if b.createdAt}<span class="hint"> {formatDate(b.createdAt)}</span>{/if}</td>
            <td class="act">
              {#if b.name !== 'main' && !internal(b)}
                {#if b.status === 'open'}
                  <button type="button" class="small" onclick={() => ((mergeFrom = b.name ?? ''), (mergeInto = b.parent || 'main'))}>Merge…</button>
                  <button type="button" class="small danger" disabled={!!busy} onclick={() => confirm(`Abandon the branch ${b.name}? It takes no change any more.`) && act(`st:${b.name}`, () => graph.setBranchStatus(namespace, b.name ?? '', 'abandoned'), `Branch ${b.name} abandoned.`)}>Abandon</button>
                {:else if b.status === 'abandoned'}
                  <button type="button" class="small" disabled={!!busy} onclick={() => act(`st:${b.name}`, () => graph.setBranchStatus(namespace, b.name ?? '', 'open'), `Branch ${b.name} reopened.`)}>Reopen</button>
                {/if}
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>

  <section class="card">
    <h3>Open a branch</h3>
    <form class="row" onsubmit={(e) => (e.preventDefault(), create())}>
      <input type="text" placeholder="name (no space)" bind:value={name} />
      <label for="br-from">from</label>
      <select id="br-from" bind:value={from}>
        {#each baselines as b (b.id)}<option value={b.id}>{b.name || shortId(b.id)} ({b.branch || 'main'})</option>{/each}
      </select>
      <button type="submit" disabled={!!busy || !name.trim() || !from}>Open</button>
    </form>
    <p class="hint">A change lands on the branch named at its creation (New change → Lands on); merging the branch brings its changes into another.</p>
  </section>

  <section class="card">
    <h3>Merge a branch</h3>
    <div class="row">
      <select bind:value={mergeFrom} aria-label="Branch to merge">
        <option value="" disabled>— branch —</option>
        {#each open.filter((b) => b.name !== 'main') as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
      </select>
      <span>into</span>
      <select bind:value={mergeInto} aria-label="Target branch">
        {#each open as b (b.name)}<option value={b.name}>{b.name}</option>{/each}
      </select>
    </div>
    {#if mergeFrom && mergeInto && mergeFrom !== mergeInto}
      {#key `${mergeFrom}>${mergeInto}`}
        <MergeResolver {namespace} from={mergeFrom} into={mergeInto} onmerge={merge} />
      {/key}
    {/if}
  </section>
</div>

<style>
  .head,
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .row select,
  .row input {
    width: auto;
    min-width: 12rem;
  }
  .check {
    display: inline-flex;
    gap: 4px;
    align-items: center;
  }
  table {
    width: 100%;
  }
  .act {
    text-align: right;
    white-space: nowrap;
  }
</style>
