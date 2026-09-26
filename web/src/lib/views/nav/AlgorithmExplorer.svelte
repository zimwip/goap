<script lang="ts">
  // Explorer of algorithms (ADR 0018): the algorithms of a domain version, grouped by type, with
  // their instances underneath. Algorithms are versioned with their domain: a published version is read-only.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { domains, refreshDomains, getDomainDraft, peekDomainDraft, domainKey, type DomainDraft } from '../../stores/domains.svelte';
  import { tabsState, tabId, closeWhere } from '../../shell/tabs.svelte';
  import { select, notify } from '../../shell/workbench.svelte';
  import { loadRaw, save } from '../../shell/storage';
  import { compareVersions, type DomainSummary } from '../../api';
  import { ALGORITHM_USAGES, emptyAlgorithm, emptyInstance, freeName } from '../../algorithmForm';
  import type { AlgorithmUsage } from '../../dsl';
  import { algorithmSpec, instanceSpec, openAlgorithm, openInstance } from '../editors/domainTabs';
  import { openContextMenu } from '../../shell/contextMenuState.svelte';

  const SEL_KEY = 'goap.ide.algorithms.domain';
  let selected = $state(String(loadRaw(SEL_KEY) ?? ''));
  let filter = $state('');

  $effect(() => {
    if (!domains.loaded) void refreshDomains();
  });

  const versions = $derived(
    [...domains.items].sort((a, b) => (a.name ?? '').localeCompare(b.name ?? '') || compareVersions(b.version, a.version)),
  );
  const vkey = (v: DomainSummary) => domainKey(v.name ?? '', v.version ?? '');

  // default: the latest draft, else the latest version of the first domain
  $effect(() => {
    if (selected && versions.some((v) => vkey(v) === selected)) return;
    const pick = versions.find((v) => v.status === 'draft') ?? versions[0];
    if (pick) selected = vkey(pick);
  });
  $effect(() => save(SEL_KEY, selected));

  const current = $derived(versions.find((v) => vkey(v) === selected));
  // the draft is created in an effect (creating it mutates state)
  let draft = $state<DomainDraft>();
  $effect(() => {
    draft = current ? (peekDomainDraft(vkey(current)) ?? getDomainDraft(current.name ?? '', current.version ?? '')) : undefined;
  });

  const q = $derived(filter.trim().toLowerCase());
  const match = (s: string) => !q || s.toLowerCase().includes(q);

  function addAlgorithm(type: AlgorithmUsage) {
    const d = draft;
    if (!d || d.readonly) return;
    d.form.algorithms.push(emptyAlgorithm(type, freeName(type.replace('_', '-'), d.form.algorithms.map((a) => a.name))));
    openAlgorithm(d, d.form.algorithms.length - 1);
  }

  function addInstance(algIndex: number) {
    const d = draft;
    if (!d || d.readonly) return;
    const a = d.form.algorithms[algIndex];
    d.form.instances.push(emptyInstance(a.name, freeName(a.name || 'instance', d.form.instances.map((i) => i.name))));
    openInstance(d, d.form.instances.length - 1);
  }

  function removeAlgorithm(i: number) {
    const d = draft;
    if (!d) return;
    const a = d.form.algorithms[i];
    const n = d.form.instances.filter((x) => x.algorithm === a.name).length;
    if (n) return void notify(`Delete the ${n} instance(s) of ${a.name || 'this algorithm'} first.`, 'error');
    if (!confirm(`Remove the algorithm "${a.name || 'unnamed'}" from the draft?`)) return;
    closeWhere((t) => t.kind === 'algorithm' && t.params.uid === a.uid);
    d.form.algorithms.splice(i, 1);
  }

  function removeInstance(i: number) {
    const d = draft;
    if (!d) return;
    const x = d.form.instances[i];
    if (!confirm(`Remove the instance "${x.name || 'unnamed'}" from the draft? Plugs that use it become invalid.`)) return;
    closeWhere((t) => t.kind === 'instance' && t.params.uid === x.uid);
    d.form.instances.splice(i, 1);
  }

  const orphans = $derived(draft ? draft.form.instances.map((x, i) => ({ x, i })).filter(({ x }) => !draft!.form.algorithms.some((a) => a.name === x.algorithm)) : []);

  function describe() {
    if (!current) return;
    select({
      title: `${current.name} v${current.version}`,
      subtitle: 'Domain (algorithms)',
      rows: [
        ['Status', current.status ?? ''],
        ['Algorithms', String(draft?.form.algorithms.length ?? 0)],
        ['Instances', String(draft?.form.instances.length ?? 0)],
      ],
    });
  }
</script>

<div class="explorer">
  <div class="tools">
    <select aria-label="Domain version" title="Domain version whose algorithms are shown" bind:value={selected} onchange={describe}>
      {#each versions as v (vkey(v))}<option value={vkey(v)}>{v.name} v{v.version} · {v.status}</option>{/each}
    </select>
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={domains.loading} onclick={() => refreshDomains()}><Icon name="refresh" size={14} /></button>
  </div>
  {#if current}
    <div class="tools">
      <input type="search" placeholder="Filter…" aria-label="Filter algorithms" bind:value={filter} data-no-pin />
      <StatusBadge status={current.status} />
    </div>
  {/if}

  {#if domains.error}<div class="alert small">{domains.error}</div>{/if}
  {#if domains.loaded && !versions.length && !domains.error}
    <p class="empty pad">No domains. Algorithms belong to a shared domain: create one in the Domains section.</p>
  {/if}

  {#if draft}
    {@const d = draft}
    {#if d.loading}
      <p class="empty pad">Loading…</p>
    {:else if d.loadError}
      <p class="alert small">{d.loadError}</p>
    {:else}
      {#if d.readonly}<p class="hint pad">Published versions are read-only: create a new domain version to edit algorithms.</p>{/if}
      <div role="tree" aria-label="Algorithms">
        {#each ALGORITHM_USAGES as u (u.usage)}
          {@const gk = `alg:${selected}:${u.usage}`}
          {@const algs = d.form.algorithms.map((a, i) => ({ a, i })).filter(({ a }) => a.type === u.usage && (match(a.name) || d.form.instances.some((x) => x.algorithm === a.name && match(x.name))))}
          <TreeRow
            icon="code"
            label={u.title}
            detail={String(d.form.algorithms.filter((a) => a.type === u.usage).length)}
            title={u.description}
            expanded={isOpen(gk, true)}
            ontoggle={() => toggle(gk, true)}
            oncontextmenu={(e) => openContextMenu(e, [{ label: `New ${u.title.toLowerCase()} algorithm`, icon: 'plus', disabled: d.readonly, run: () => addAlgorithm(u.usage) }])}
          >
            {#snippet actions()}
              {#if !d.readonly}
                <button type="button" title="Add" aria-label="Add: {u.title} algorithm" onclick={(e) => { e.stopPropagation(); addAlgorithm(u.usage); }}><Icon name="plus" size={13} /></button>
              {/if}
            {/snippet}
          </TreeRow>
          {#if isOpen(gk, true)}
            {#each algs as { a, i } (a.uid)}
              {@const ak = `alga:${a.uid}`}
              {@const insts = d.form.instances.map((x, k) => ({ x, k })).filter(({ x }) => x.algorithm === a.name && a.name)}
              <TreeRow
                depth={1}
                icon="zap"
                label={a.name || '(unnamed)'}
                italic={!a.name}
                detail={a.type === 'adapter' && a.connector ? `${a.mcp || '?'} → ${a.connector}` : a.language}
                expanded={isOpen(ak, true)}
                active={tabsState.active === tabId(algorithmSpec(d.name, d.version, a.uid, a.name))}
                badge={d.count(`algorithms[${i}]`) || undefined}
                badgeTone="danger"
                title={a.description || a.name}
                onselect={() => openAlgorithm(d, i, false)}
                onopen={() => openAlgorithm(d, i, true)}
                ontoggle={() => toggle(ak, true)}
                oncontextmenu={(e) =>
                  openContextMenu(e, [
                    { label: 'New instance', icon: 'plus', disabled: d.readonly || a.type === 'adapter', run: () => addInstance(i) },
                    { label: 'Remove algorithm', icon: 'trash', disabled: d.readonly, run: () => removeAlgorithm(i) },
                  ])}
              >
                {#snippet actions()}
                  {#if !d.readonly && a.type !== 'adapter'}
                    <button type="button" title="New instance" aria-label="New instance of {a.name}" onclick={(e) => { e.stopPropagation(); addInstance(i); }}><Icon name="plus" size={13} /></button>
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(ak, true)}
                {#each insts as { x, k } (x.uid)}
                  <TreeRow
                    depth={2}
                    icon="tag"
                    label={x.name || '(unnamed)'}
                    italic={!x.name}
                    active={tabsState.active === tabId(instanceSpec(d.name, d.version, x.uid, x.name))}
                    badge={d.count(`algorithmInstances[${k}]`) || undefined}
                    badgeTone="danger"
                    onselect={() => openInstance(d, k, false)}
                    onopen={() => openInstance(d, k, true)}
                    oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove instance', icon: 'trash', disabled: d.readonly, run: () => removeInstance(k) }])}
                  />
                {/each}
              {/if}
            {:else}
              <p class="empty pad3">No {u.title.toLowerCase()} algorithms.</p>
            {/each}
          {/if}
        {/each}
        {#if orphans.length}
          <TreeRow icon="alert" label="Unresolved instances" detail={String(orphans.length)} expanded={true} />
          {#each orphans as { x, i } (x.uid)}
            <TreeRow depth={1} icon="tag" label={x.name || '(unnamed)'} detail={`algorithm ${x.algorithm || '?'}`} badge={d.count(`algorithmInstances[${i}]`) || undefined} badgeTone="danger" onselect={() => openInstance(d, i, false)} onopen={() => openInstance(d, i, true)} />
          {/each}
        {/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    align-items: center;
    padding: 0 0.5rem 0.4rem;
  }
  .tools select {
    min-height: 24px;
    height: 24px;
    padding: 0 0.3rem;
  }
  .tools input {
    min-height: 24px;
    height: 24px;
    margin-right: 0.2rem;
  }
  .tools button {
    padding: 0.1rem 0.3rem;
  }
  .pad {
    padding: 0.4rem 0.8rem;
  }
  .pad3 {
    padding: 0.1rem 0 0.1rem 46px;
    margin: 0;
    font-size: 0.9em;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
