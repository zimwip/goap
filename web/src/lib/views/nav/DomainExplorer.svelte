<script lang="ts">
  // Explorer of domains (one per namespace): domain → version → node types / link types.
  import Icon from '../../shell/Icon.svelte';
  import TreeRow from '../TreeRow.svelte';
  import StatusBadge from '../../components/StatusBadge.svelte';
  import { toggle, isOpen } from './expanded.svelte';
  import { domains, refreshDomains, groupedDomains, getDomainDraft, peekDomainDraft, domainKey } from '../../stores/domains.svelte';
  import { openTab, tabsState, tabId, closeWhere } from '../../shell/tabs.svelte';
  import { select, notify } from '../../shell/workbench.svelte';
  import { formatDate, type DomainSummary } from '../../api';
  import { domainSpec, openDomain, revealDomainPath, algorithmSpec, instanceSpec, openAlgorithm, openInstance, openEnum, openLifecycle, nodeTypeSpec, linkTypeSpec, enumSpec } from '../editors/domainTabs';
  import { ALGORITHM_USAGES, emptyAlgorithm, emptyInstance, freeName } from '../../algorithmForm';
  import type { AlgorithmUsage } from '../../dsl';
  import type { DomainDraft } from '../../stores/domains.svelte';
  import { emptyNodeType, emptyLinkType, emptyAttribute, emptyEnum, defaultLifecycle } from '../../methodologyForm';
  import { openContextMenu } from '../../shell/contextMenuState.svelte';
  import { confirmDialog } from '../../shell/confirmState.svelte';

  let filter = $state('');

  $effect(() => {
    if (!domains.loaded) void refreshDomains();
  });

  const groups = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    const all = groupedDomains();
    if (!q) return all;
    return all.filter((g) => `${g.name} ${g.description}`.toLowerCase().includes(q));
  });

  const vkey = (v: DomainSummary) => domainKey(v.name ?? '', v.version ?? '');

  function toggleVersion(v: DomainSummary) {
    const k = `dv:${vkey(v)}`;
    toggle(k);
    if (isOpen(k)) void getDomainDraft(v.name ?? '', v.version ?? '');
  }

  // Expanded versions (restored state) load their draft.
  $effect(() => {
    for (const v of domains.items) if (isOpen(`dv:${vkey(v)}`)) getDomainDraft(v.name ?? '', v.version ?? '');
  });

  function selectVersion(v: DomainSummary, pin = false) {
    openDomain(v.name ?? '', v.version ?? '', pin);
    select({
      title: `${v.name} v${v.version}`,
      subtitle: 'Domain',
      rows: [
        ['Status', v.status ?? ''],
        ['Description', v.description ?? ''],
        ['Node types', String(v.nodeTypeCount ?? 0)],
        ['Link types', String(v.linkTypeCount ?? 0)],
        ['Modified', formatDate(v.updatedAt)],
        ['Published', formatDate(v.publishedAt)],
      ],
    });
  }

  function addNodeType(v: DomainSummary) {
    const d = getDomainDraft(v.name ?? '', v.version ?? '');
    d.form.nodeTypes.push(emptyNodeType());
    revealDomainPath(d.name, d.version, `nodeTypes[${d.form.nodeTypes.length - 1}]`);
  }

  function addLinkType(v: DomainSummary) {
    const d = getDomainDraft(v.name ?? '', v.version ?? '');
    d.form.linkTypes.push(emptyLinkType());
    revealDomainPath(d.name, d.version, `linkTypes[${d.form.linkTypes.length - 1}]`);
  }

  // attributes and enums
  function addAttribute(d: DomainDraft, kind: 'nodeTypes' | 'linkTypes', i: number) {
    if (d.readonly) return;
    const attrs = d.form[kind][i].attributes;
    attrs.push(emptyAttribute(freeName('attribute', attrs.map((a) => a.name))));
    revealDomainPath(d.name, d.version, `${kind}[${i}].attributes[${attrs.length - 1}]`);
  }

  function addEnum(d: DomainDraft) {
    if (d.readonly) return;
    d.form.enums.push(emptyEnum(freeName('enum', d.form.enums.map((e) => e.name))));
    openEnum(d, d.form.enums.length - 1);
  }

  async function removeEnum(d: DomainDraft, i: number) {
    const e = d.form.enums[i];
    const n = [...d.form.nodeTypes, ...d.form.linkTypes].reduce((c, t) => c + t.attributes.filter((a) => a.enum === e.name && e.name).length, 0);
    if (n) return void notify(`${n} attribute(s) use ${e.name}: change them first.`, 'error');
    if (!(await confirmDialog({ message: `Remove the enum "${e.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    closeWhere((t) => t.kind === 'enum' && t.params.uid === e.uid);
    d.form.enums.splice(i, 1);
  }

  /** opens the instance an attribute, node type or transition plugs, by name */
  function openInstanceNamed(d: DomainDraft, name: string, pin = false) {
    const i = d.form.instances.findIndex((x) => x.name === name);
    if (i >= 0) openInstance(d, i, pin);
  }

  // lifecycles: one entry each, its states and transitions underneath
  function addLifecycle(d: DomainDraft) {
    if (d.readonly) return;
    const taken = d.form.lifecycles.map((l) => l.name);
    d.form.lifecycles.push(defaultLifecycle(freeName('lifecycle', taken)));
    revealDomainPath(d.name, d.version, `lifecycles[${d.form.lifecycles.length - 1}]`);
  }

  function addState(d: DomainDraft, li: number) {
    if (d.readonly) return;
    const lc = d.form.lifecycles[li];
    lc.states.push({ name: '', description: '', notLandable: false, final: false });
    revealDomainPath(d.name, d.version, `lifecycles[${li}].states[${lc.states.length - 1}]`);
  }

  function addTransition(d: DomainDraft, li: number) {
    if (d.readonly) return;
    const lc = d.form.lifecycles[li];
    const names = lc.states.map((s) => s.name.trim()).filter(Boolean);
    lc.transitions.push({ name: '', description: '', from: names[0] ?? '', to: names[1] ?? '', permission: '', guard: '', requiresAttributes: '', requiresLinks: '', children: '', guards: [], actions: [] });
    revealDomainPath(d.name, d.version, `lifecycles[${li}].transitions[${lc.transitions.length - 1}]`);
  }

  async function removeLifecycle(d: DomainDraft, i: number) {
    const l = d.form.lifecycles[i];
    const n = d.form.nodeTypes.filter((t) => t.lifecycle === l.name && l.name).length;
    if (n) return void notify(`${n} node type(s) use ${l.name}: change their lifecycle first.`, 'error');
    if (!(await confirmDialog({ message: `Remove the lifecycle "${l.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    closeWhere((t) => t.kind === 'lifecycle' && t.params.uid === l.uid);
    d.form.lifecycles.splice(i, 1);
  }

  async function removeItem(d: DomainDraft, list: unknown[], i: number, what: string, name: string) {
    if (!(await confirmDialog({ message: `Remove the ${what} "${name || 'unnamed'}" from the draft?`, danger: true }))) return;
    list.splice(i, 1);
  }

  // algorithms (ADR 0018): grouped by usage, with their instances underneath
  function addAlgorithm(d: DomainDraft, type: AlgorithmUsage) {
    if (d.readonly) return;
    d.form.algorithms.push(emptyAlgorithm(type, freeName(type.replace('_', '-'), d.form.algorithms.map((a) => a.name))));
    openAlgorithm(d, d.form.algorithms.length - 1);
  }

  function addInstance(d: DomainDraft, algIndex: number) {
    if (d.readonly) return;
    const a = d.form.algorithms[algIndex];
    d.form.instances.push(emptyInstance(a.name, freeName(a.name || 'instance', d.form.instances.map((i) => i.name))));
    openInstance(d, d.form.instances.length - 1);
  }

  async function removeAlgorithm(d: DomainDraft, i: number) {
    const a = d.form.algorithms[i];
    const n = d.form.instances.filter((x) => x.algorithm === a.name).length;
    if (n) return void notify(`Delete the ${n} instance(s) of ${a.name || 'this algorithm'} first.`, 'error');
    if (!(await confirmDialog({ message: `Remove the algorithm "${a.name || 'unnamed'}" from the draft?`, danger: true }))) return;
    closeWhere((t) => t.kind === 'algorithm' && t.params.uid === a.uid);
    d.form.algorithms.splice(i, 1);
  }

  async function removeInstance(d: DomainDraft, i: number) {
    const x = d.form.instances[i];
    if (!(await confirmDialog({ message: `Remove the instance "${x.name || 'unnamed'}" from the draft? Plugs that use it become invalid.`, danger: true })))
      return;
    closeWhere((t) => t.kind === 'instance' && t.params.uid === x.uid);
    d.form.instances.splice(i, 1);
  }

  const orphansOf = (d: DomainDraft) =>
    d.form.instances.map((x, i) => ({ x, i })).filter(({ x }) => !d.form.algorithms.some((a) => a.name === x.algorithm));
</script>

<div class="explorer">
  <div class="tools">
    <input type="search" placeholder="Filter…" aria-label="Filter domains" bind:value={filter} data-no-pin />
    <button type="button" class="ghost small" title="New domain" aria-label="New domain" onclick={() => openTab(domainSpec('', ''), { pin: true })}
      ><Icon name="plus" size={14} /></button
    >
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={domains.loading} onclick={() => refreshDomains()}
      ><Icon name="refresh" size={14} /></button
    >
  </div>

  {#if domains.error}<div class="alert small">{domains.error}</div>{/if}
  {#if domains.loaded && !domains.items.length && !domains.error}
    <p class="empty pad">No domains. Create one: a domain is a namespace, with its node and link types; methodologies act on it.</p>
  {/if}

  <div role="tree" aria-label="Domains">
    {#each groups as g (g.name)}
      {@const gk = `dm:${g.name}`}
      <TreeRow icon="graph" label={g.name} detail={g.builtin ? 'built-in' : ''} expanded={isOpen(gk, true)} title={g.builtin ? `${g.name}: changes with the platform code` : g.description || g.name} ontoggle={() => toggle(gk, true)} />
      {#if isOpen(gk, true)}
        {#each g.versions as v (v.version)}
          {@const k = vkey(v)}
          {@const d = peekDomainDraft(k)}
          {@const vOpen = isOpen(`dv:${k}`)}
          {@const vid = tabId(domainSpec(v.name ?? '', v.version ?? ''))}
          <TreeRow
            depth={1}
            icon="tag"
            label={`v${v.version}`}
            detail={`${v.nodeTypeCount ?? 0} types · ${v.linkTypeCount ?? 0} links`}
            expanded={vOpen}
            active={tabsState.active === vid}
            badge={d?.dirty ? '●' : d && d.allIssues.length ? d.allIssues.length : undefined}
            badgeTone={d?.dirty ? 'accent' : 'danger'}
            onselect={() => selectVersion(v)}
            onopen={() => selectVersion(v, true)}
            ontoggle={() => toggleVersion(v)}
          >
            {#snippet trail()}
              <StatusBadge status={v.status} />
            {/snippet}
          </TreeRow>
          {#if vOpen}
            {#if !d || d.loading}
              <p class="empty pad2">Loading…</p>
            {:else if d.loadError}
              <p class="alert small">{d.loadError}</p>
            {:else}
              {@const ntK = `dn:${k}`}
              <TreeRow
                depth={2}
                icon="node"
                label="Node types"
                detail={String(d.form.nodeTypes.length)}
                expanded={isOpen(ntK, true)}
                badge={d.count('nodeTypes') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(ntK, true)}
                oncontextmenu={(e) => openContextMenu(e, [{ label: 'New node type', icon: 'plus', disabled: d.readonly, run: () => addNodeType(v) }])}
              >
                {#snippet actions()}
                  {#if !d.readonly}
                    <button type="button" title="Add" aria-label="Add: Node type" onclick={(e) => { e.stopPropagation(); addNodeType(v); }}
                      ><Icon name="plus" size={13} /></button
                    >
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(ntK, true)}
                {#each d.form.nodeTypes as n, i (n.uid)}
                  {@const nk = `dnt:${n.uid}`}
                  <TreeRow
                    depth={3}
                    icon="node"
                    label={n.name || '(unnamed)'}
                    italic={!n.name}
                    detail={n.extends ? `extends ${n.extends}` : ''}
                    expanded={isOpen(nk, false)}
                    ontoggle={() => toggle(nk, false)}
                    active={tabsState.active === tabId(nodeTypeSpec(d.name, d.version, n.uid, n.name))}
                    badge={d.count(`nodeTypes[${i}]`) || undefined}
                    badgeTone="danger"
                    onselect={() => revealDomainPath(d.name, d.version, `nodeTypes[${i}]`)}
                    onopen={() => revealDomainPath(d.name, d.version, `nodeTypes[${i}]`, true)}
                    oncontextmenu={(e) =>
                      openContextMenu(e, [
                        {
                          label: 'Remove node type',
                          icon: 'trash',
                          disabled: d.readonly,
                          run: async () => {
                            if (await confirmDialog({ message: `Remove the node type "${n.name || 'unnamed'}" from the draft?`, danger: true })) {
                              closeWhere((t) => t.kind === 'nodetype' && t.params.uid === n.uid);
                              d.form.nodeTypes.splice(i, 1);
                            }
                          },
                        },
                        { label: 'New attribute', icon: 'plus', disabled: d.readonly, run: () => addAttribute(d, 'nodeTypes', i) },
                      ])}
                  >
                    {#snippet actions()}
                      {#if !d.readonly}
                        <button type="button" title="New attribute" aria-label="New attribute of {n.name}" onclick={(e) => { e.stopPropagation(); addAttribute(d, 'nodeTypes', i); }}
                          ><Icon name="plus" size={13} /></button
                        >
                      {/if}
                    {/snippet}
                  </TreeRow>
                  {#if isOpen(nk, false)}
                    {#each n.attributes as a, j (a.uid)}
                      {@const ak = `dna:${a.uid}`}
                      <TreeRow
                        depth={4}
                        icon="tag"
                        label={a.name || '(unnamed)'}
                        italic={!a.name}
                        detail={[a.type || 'untyped', a.asName ? 'name' : '', a.type === 'enum' && a.enum ? a.enum : ''].filter(Boolean).join(' · ')}
                        title={a.label || a.name}
                        expanded={a.validators.length ? isOpen(ak, true) : undefined}
                        ontoggle={() => toggle(ak, true)}
                        badge={d.count(`nodeTypes[${i}].attributes[${j}]`) || undefined}
                        badgeTone="danger"
                        onselect={() => revealDomainPath(d.name, d.version, `nodeTypes[${i}].attributes[${j}]`)}
                        onopen={() => revealDomainPath(d.name, d.version, `nodeTypes[${i}].attributes[${j}]`, true)}
                        oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove attribute', icon: 'trash', disabled: d.readonly, run: () => removeItem(d, n.attributes, j, 'attribute', a.name) }])}
                      />
                      {#if a.validators.length && isOpen(ak, true)}
                        {#each a.validators as v, k (k)}
                          <TreeRow depth={5} icon="check" label={v || '(none)'} italic={!v} detail="validator" onselect={() => openInstanceNamed(d, v)} onopen={() => openInstanceNamed(d, v, true)} />
                        {/each}
                      {/if}
                    {/each}
                    {#each n.validators as v, k (k)}
                      <TreeRow depth={4} icon="check" label={v || '(none)'} italic={!v} detail="node validator" onselect={() => openInstanceNamed(d, v)} onopen={() => openInstanceNamed(d, v, true)} />
                    {/each}
                    {#if n.lifecycle}
                      {@const li = d.form.lifecycles.findIndex((lc) => lc.name === n.lifecycle)}
                      <TreeRow depth={4} icon="runs" label={n.lifecycle} detail="lifecycle" onselect={() => li >= 0 && openLifecycle(d, li, false)} onopen={() => li >= 0 && openLifecycle(d, li, true)} />
                    {/if}
                    {#if !n.attributes.length && !n.validators.length}<p class="empty pad4">No attributes.</p>{/if}
                  {/if}
                {:else}
                  <p class="empty pad3">No node types.</p>
                {/each}
              {/if}
              {@const ltK = `dl:${k}`}
              <TreeRow
                depth={2}
                icon="trace"
                label="Link types"
                detail={String(d.form.linkTypes.length)}
                expanded={isOpen(ltK, true)}
                badge={d.count('linkTypes') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(ltK, true)}
                oncontextmenu={(e) => openContextMenu(e, [{ label: 'New link type', icon: 'plus', disabled: d.readonly, run: () => addLinkType(v) }])}
              >
                {#snippet actions()}
                  {#if !d.readonly}
                    <button type="button" title="Add" aria-label="Add: Link type" onclick={(e) => { e.stopPropagation(); addLinkType(v); }}
                      ><Icon name="plus" size={13} /></button
                    >
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(ltK, true)}
                {#each d.form.linkTypes as l, i (l.uid)}
                  {@const lk2 = `dlt:${l.uid}`}
                  <TreeRow
                    depth={3}
                    icon="trace"
                    label={l.name || '(unnamed)'}
                    italic={!l.name}
                    detail={l.from && l.to ? `${l.from} → ${l.to}` : ''}
                    expanded={isOpen(lk2, false)}
                    ontoggle={() => toggle(lk2, false)}
                    active={tabsState.active === tabId(linkTypeSpec(d.name, d.version, l.uid, l.name))}
                    badge={d.count(`linkTypes[${i}]`) || undefined}
                    badgeTone="danger"
                    onselect={() => revealDomainPath(d.name, d.version, `linkTypes[${i}]`)}
                    onopen={() => revealDomainPath(d.name, d.version, `linkTypes[${i}]`, true)}
                    oncontextmenu={(e) =>
                      openContextMenu(e, [
                        {
                          label: 'Remove link type',
                          icon: 'trash',
                          disabled: d.readonly,
                          run: async () => {
                            if (await confirmDialog({ message: `Remove the link type "${l.name || 'unnamed'}" from the draft?`, danger: true })) {
                              closeWhere((t) => t.kind === 'linktype' && t.params.uid === l.uid);
                              d.form.linkTypes.splice(i, 1);
                            }
                          },
                        },
                        { label: 'New attribute', icon: 'plus', disabled: d.readonly, run: () => addAttribute(d, 'linkTypes', i) },
                      ])}
                  >
                    {#snippet actions()}
                      {#if !d.readonly}
                        <button type="button" title="New attribute" aria-label="New attribute of {l.name}" onclick={(e) => { e.stopPropagation(); addAttribute(d, 'linkTypes', i); }}
                          ><Icon name="plus" size={13} /></button
                        >
                      {/if}
                    {/snippet}
                  </TreeRow>
                  {#if isOpen(lk2, false)}
                    {#each l.attributes as a, j (a.uid)}
                      {@const ak = `dla:${a.uid}`}
                      <TreeRow
                        depth={4}
                        icon="tag"
                        label={a.name || '(unnamed)'}
                        italic={!a.name}
                        detail={[a.type || 'untyped', a.type === 'enum' && a.enum ? a.enum : ''].filter(Boolean).join(' · ')}
                        title={a.label || a.name}
                        expanded={a.validators.length ? isOpen(ak, true) : undefined}
                        ontoggle={() => toggle(ak, true)}
                        badge={d.count(`linkTypes[${i}].attributes[${j}]`) || undefined}
                        badgeTone="danger"
                        onselect={() => revealDomainPath(d.name, d.version, `linkTypes[${i}].attributes[${j}]`)}
                        onopen={() => revealDomainPath(d.name, d.version, `linkTypes[${i}].attributes[${j}]`, true)}
                        oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove attribute', icon: 'trash', disabled: d.readonly, run: () => removeItem(d, l.attributes, j, 'attribute', a.name) }])}
                      />
                      {#if a.validators.length && isOpen(ak, true)}
                        {#each a.validators as v, k (k)}
                          <TreeRow depth={5} icon="check" label={v || '(none)'} italic={!v} detail="validator" onselect={() => openInstanceNamed(d, v)} onopen={() => openInstanceNamed(d, v, true)} />
                        {/each}
                      {/if}
                    {:else}
                      <p class="empty pad4">No attributes.</p>
                    {/each}
                  {/if}
                {:else}
                  <p class="empty pad3">No link types.</p>
                {/each}
              {/if}
              {@const enK = `de:${k}`}
              <TreeRow
                depth={2}
                icon="list"
                label="Enums"
                detail={String(d.form.enums.length)}
                expanded={isOpen(enK, true)}
                badge={d.count('enums') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(enK, true)}
                oncontextmenu={(e) => openContextMenu(e, [{ label: 'New enum', icon: 'plus', disabled: d.readonly, run: () => addEnum(d) }])}
              >
                {#snippet actions()}
                  {#if !d.readonly}
                    <button type="button" title="Add" aria-label="Add: Enum" onclick={(e) => { e.stopPropagation(); addEnum(d); }}
                      ><Icon name="plus" size={13} /></button
                    >
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(enK, true)}
                {#each d.form.enums as en, i (en.uid)}
                  {@const ek = `den:${en.uid}`}
                  <TreeRow
                    depth={3}
                    icon="list"
                    label={en.name || '(unnamed)'}
                    italic={!en.name}
                    detail={`${en.values.length} values`}
                    expanded={isOpen(ek, false)}
                    ontoggle={() => toggle(ek, false)}
                    active={tabsState.active === tabId(enumSpec(d.name, d.version, en.uid, en.name))}
                    badge={d.count(`enums[${i}]`) || undefined}
                    badgeTone="danger"
                    onselect={() => openEnum(d, i, false)}
                    onopen={() => openEnum(d, i, true)}
                    oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove enum', icon: 'trash', disabled: d.readonly, run: () => removeEnum(d, i) }])}
                  />
                  {#if isOpen(ek, false)}
                    {#each en.values as v, j (j)}
                      <TreeRow depth={4} label={v.value || '(empty)'} italic={!v.value} detail={v.label} onselect={() => revealDomainPath(d.name, d.version, `enums[${i}].values[${j}]`)} onopen={() => revealDomainPath(d.name, d.version, `enums[${i}].values[${j}]`, true)} />
                    {/each}
                  {/if}
                {:else}
                  <p class="empty pad3">No enums.</p>
                {/each}
              {/if}
              {@const lcK = `dc:${k}`}
              <TreeRow
                depth={2}
                icon="runs"
                label="Lifecycles"
                detail={String(d.form.lifecycles.length)}
                expanded={isOpen(lcK, true)}
                badge={d.count('lifecycles') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(lcK, true)}
                oncontextmenu={(e) => openContextMenu(e, [{ label: 'New lifecycle', icon: 'plus', disabled: d.readonly, run: () => addLifecycle(d) }])}
              >
                {#snippet actions()}
                  {#if !d.readonly}
                    <button type="button" title="Add" aria-label="Add: Lifecycle" onclick={(e) => { e.stopPropagation(); addLifecycle(d); }}
                      ><Icon name="plus" size={13} /></button
                    >
                  {/if}
                {/snippet}
              </TreeRow>
              {#if isOpen(lcK, true)}
                {#each d.form.lifecycles as l, i (i)}
                  {@const lk = `dcl:${k}:${i}`}
                  {@const lp = `lifecycles[${i}]`}
                  <TreeRow
                    depth={3}
                    icon="runs"
                    label={l.name || '(unnamed)'}
                    italic={!l.name}
                    detail={`${l.states.length} states · ${l.transitions.length} transitions`}
                    expanded={isOpen(lk, true)}
                    badge={d.count(lp) || undefined}
                    badgeTone="danger"
                    onselect={() => revealDomainPath(d.name, d.version, lp)}
                    onopen={() => revealDomainPath(d.name, d.version, lp, true)}
                    ontoggle={() => toggle(lk, true)}
                    oncontextmenu={(e) =>
                      openContextMenu(e, [
                        { label: 'New state', icon: 'plus', disabled: d.readonly, run: () => addState(d, i) },
                        { label: 'New transition', icon: 'plus', disabled: d.readonly, run: () => addTransition(d, i) },
                        { label: 'Remove lifecycle', icon: 'trash', disabled: d.readonly, run: () => removeLifecycle(d, i) },
                      ])}
                  />
                  {#if isOpen(lk, true)}
                    {@const stK = `dcs:${k}:${i}`}
                    <TreeRow
                      depth={4}
                      icon="tag"
                      label="States"
                      detail={String(l.states.length)}
                      expanded={isOpen(stK, true)}
                      ontoggle={() => toggle(stK, true)}
                      oncontextmenu={(e) => openContextMenu(e, [{ label: 'New state', icon: 'plus', disabled: d.readonly, run: () => addState(d, i) }])}
                    >
                      {#snippet actions()}
                        {#if !d.readonly}
                          <button type="button" title="Add" aria-label="Add: State" onclick={(e) => { e.stopPropagation(); addState(d, i); }}
                            ><Icon name="plus" size={13} /></button
                          >
                        {/if}
                      {/snippet}
                    </TreeRow>
                    {#if isOpen(stK, true)}
                      {#each l.states as st, j (j)}
                        <TreeRow
                          depth={5}
                          label={st.name || '(unnamed)'}
                          italic={!st.name}
                          detail={[l.initial && l.initial === st.name ? 'initial' : '', st.notLandable ? 'not landable' : '', st.final ? 'final' : ''].filter(Boolean).join(' · ')}
                          badge={d.count(`${lp}.states[${j}]`) || undefined}
                          badgeTone="danger"
                          onselect={() => revealDomainPath(d.name, d.version, `${lp}.states[${j}]`)}
                          onopen={() => revealDomainPath(d.name, d.version, `${lp}.states[${j}]`, true)}
                          oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove state', icon: 'trash', disabled: d.readonly, run: () => removeItem(d, l.states, j, 'state', st.name) }])}
                        />
                      {:else}
                        <p class="empty pad4">No states.</p>
                      {/each}
                    {/if}
                    {@const trK = `dct:${k}:${i}`}
                    <TreeRow
                      depth={4}
                      icon="trace"
                      label="Transitions"
                      detail={String(l.transitions.length)}
                      expanded={isOpen(trK, true)}
                      ontoggle={() => toggle(trK, true)}
                      oncontextmenu={(e) => openContextMenu(e, [{ label: 'New transition', icon: 'plus', disabled: d.readonly, run: () => addTransition(d, i) }])}
                    >
                      {#snippet actions()}
                        {#if !d.readonly}
                          <button type="button" title="Add" aria-label="Add: Transition" onclick={(e) => { e.stopPropagation(); addTransition(d, i); }}
                            ><Icon name="plus" size={13} /></button
                          >
                        {/if}
                      {/snippet}
                    </TreeRow>
                    {#if isOpen(trK, true)}
                      {#each l.transitions as tr, j (j)}
                        {@const tk = `dctr:${l.uid}:${j}`}
                        {@const plugs = tr.guards.length + tr.actions.length}
                        <TreeRow
                          depth={5}
                          label={tr.name || '(unnamed)'}
                          italic={!tr.name}
                          detail={[tr.from || tr.to ? `${tr.from || '?'} → ${tr.to || '?'}` : '', tr.permission, tr.guard ? 'CEL guard' : ''].filter(Boolean).join(' · ')}
                          expanded={plugs ? isOpen(tk, true) : undefined}
                          ontoggle={() => toggle(tk, true)}
                          badge={d.count(`${lp}.transitions[${j}]`) || undefined}
                          badgeTone="danger"
                          onselect={() => revealDomainPath(d.name, d.version, `${lp}.transitions[${j}]`)}
                          onopen={() => revealDomainPath(d.name, d.version, `${lp}.transitions[${j}]`, true)}
                          oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove transition', icon: 'trash', disabled: d.readonly, run: () => removeItem(d, l.transitions, j, 'transition', tr.name) }])}
                        />
                        {#if plugs && isOpen(tk, true)}
                          {#each tr.guards as g, k (`g${k}`)}
                            <TreeRow depth={6} icon="check" label={g || '(none)'} italic={!g} detail="guard" onselect={() => openInstanceNamed(d, g)} onopen={() => openInstanceNamed(d, g, true)} />
                          {/each}
                          {#each tr.actions as a, k (`a${k}`)}
                            <TreeRow depth={6} icon="zap" label={a || '(none)'} italic={!a} detail="action" onselect={() => openInstanceNamed(d, a)} onopen={() => openInstanceNamed(d, a, true)} />
                          {/each}
                        {/if}
                      {:else}
                        <p class="empty pad4">No transitions.</p>
                      {/each}
                    {/if}
                  {/if}
                {:else}
                  <p class="empty pad3">No lifecycles.</p>
                {/each}
              {/if}
              {@const agK = `dg:${k}`}
              <TreeRow
                depth={2}
                icon="code"
                label="Algorithms"
                detail={String(d.form.algorithms.length)}
                expanded={isOpen(agK, true)}
                badge={d.count('algorithms') + d.count('algorithmInstances') || undefined}
                badgeTone="danger"
                ontoggle={() => toggle(agK, true)}
              />
              {#if isOpen(agK, true)}
                {#each ALGORITHM_USAGES.filter((x) => x.usage !== 'adapter') as u (u.usage)}
                  {@const uk = `alg:${k}:${u.usage}`}
                  {@const algs = d.form.algorithms.map((a, i) => ({ a, i })).filter(({ a }) => a.type === u.usage)}
                  <TreeRow
                    depth={3}
                    icon="code"
                    label={u.title}
                    detail={String(algs.length)}
                    title={u.description}
                    expanded={isOpen(uk, true)}
                    ontoggle={() => toggle(uk, true)}
                    oncontextmenu={(e) => openContextMenu(e, [{ label: `New ${u.title.toLowerCase()} algorithm`, icon: 'plus', disabled: d.readonly, run: () => addAlgorithm(d, u.usage) }])}
                  >
                    {#snippet actions()}
                      {#if !d.readonly}
                        <button type="button" title="Add" aria-label="Add: {u.title} algorithm" onclick={(e) => { e.stopPropagation(); addAlgorithm(d, u.usage); }}
                          ><Icon name="plus" size={13} /></button
                        >
                      {/if}
                    {/snippet}
                  </TreeRow>
                  {#if isOpen(uk, true)}
                    {#each algs as { a, i } (a.uid)}
                      {@const ak = `alga:${a.uid}`}
                      {@const insts = d.form.instances.map((x, j) => ({ x, j })).filter(({ x }) => x.algorithm === a.name && a.name)}
                      <TreeRow
                        depth={4}
                        icon="zap"
                        label={a.name || '(unnamed)'}
                        italic={!a.name}
                        detail={a.language}
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
                            { label: 'New instance', icon: 'plus', disabled: d.readonly, run: () => addInstance(d, i) },
                            { label: 'Remove algorithm', icon: 'trash', disabled: d.readonly, run: () => removeAlgorithm(d, i) },
                          ])}
                      >
                        {#snippet actions()}
                          {#if !d.readonly}
                            <button type="button" title="New instance" aria-label="New instance of {a.name}" onclick={(e) => { e.stopPropagation(); addInstance(d, i); }}
                              ><Icon name="plus" size={13} /></button
                            >
                          {/if}
                        {/snippet}
                      </TreeRow>
                      {#if isOpen(ak, true)}
                        {#each insts as { x, j } (x.uid)}
                          <TreeRow
                            depth={5}
                            icon="tag"
                            label={x.name || '(unnamed)'}
                            italic={!x.name}
                            active={tabsState.active === tabId(instanceSpec(d.name, d.version, x.uid, x.name))}
                            badge={d.count(`algorithmInstances[${j}]`) || undefined}
                            badgeTone="danger"
                            onselect={() => openInstance(d, j, false)}
                            onopen={() => openInstance(d, j, true)}
                            oncontextmenu={(e) => openContextMenu(e, [{ label: 'Remove instance', icon: 'trash', disabled: d.readonly, run: () => removeInstance(d, j) }])}
                          />
                        {/each}
                      {/if}
                    {:else}
                      <p class="empty pad4">No {u.title.toLowerCase()} algorithms.</p>
                    {/each}
                  {/if}
                {/each}
                {#each [orphansOf(d)] as orphans}
                  {#if orphans.length}
                    <TreeRow depth={3} icon="alert" label="Unresolved instances" detail={String(orphans.length)} expanded={true} />
                    {#each orphans as { x, i } (x.uid)}
                      <TreeRow depth={4} icon="tag" label={x.name || '(unnamed)'} detail={`algorithm ${x.algorithm || '?'}`} badge={d.count(`algorithmInstances[${i}]`) || undefined} badgeTone="danger" onselect={() => openInstance(d, i, false)} onopen={() => openInstance(d, i, true)} />
                    {/each}
                  {/if}
                {/each}
              {/if}
            {/if}
          {/if}
        {/each}
      {/if}
    {/each}
  </div>
</div>

<style>
  .explorer {
    padding-bottom: 1rem;
  }
  .tools {
    display: flex;
    gap: 2px;
    padding: 0 0.5rem 0.4rem;
    position: sticky;
    top: 0;
    background: var(--chrome);
    z-index: 1;
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
  .pad2 {
    padding: 0.1rem 0 0.1rem 46px;
    margin: 0;
  }
  .pad3 {
    padding: 0.1rem 0 0.1rem 58px;
    margin: 0;
    font-size: 0.9em;
  }
  .pad4 {
    padding: 0.1rem 0 0.1rem 70px;
    margin: 0;
    font-size: 0.9em;
  }
  .alert.small {
    margin: 0.3rem 0.5rem;
    font-size: 0.88em;
  }
</style>
