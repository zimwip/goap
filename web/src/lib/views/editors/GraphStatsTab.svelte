<script lang="ts">
  // "Statistics" tab of a namespace (graph statistics tool): the shape of the state at the head of a lasting branch
  // (nodes, links, degrees, connected parts, per node type and link type, the most connected nodes) and what the store
  // holds for the namespace (versions, link rows, baselines, changes, log entries). Read when opened and when a baseline
  // is written; nothing is stored.
  import type { Tab } from '../../shell/types';
  import Icon from '../../shell/Icon.svelte';
  import { stamp, keyOf } from '../../flux/signals.svelte';
  import { openNode } from '../../nodeEditors';
  import { graph, errorMessage, formatDate, type GraphStats } from '../../api';
  import { byStream, fmt, insights, measure, num, pct, ranked, shortType, total, versionHistogram } from '../../graphStats';

  let { tab }: { tab: Tab } = $props();

  const namespace = $derived(tab.params.namespace ?? '');
  let branch = $state('main');
  let stats = $state<GraphStats | undefined>();
  let loading = $state(false);
  let error = $state('');

  async function load(ns: string, br: string) {
    loading = true;
    try {
      stats = (await graph.graphStats(ns, br)).stats;
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    void stamp(keyOf.baselines);
    void load(namespace, branch);
  });

  const st = $derived(stats?.state);
  const lines = $derived(insights(stats));
  const branches = $derived(stats?.branches?.names ?? ['main']);
  const typeMax = $derived(Math.max(1, ...(st?.types ?? []).map((t) => num(t.nodes))));
  const linkMax = $derived(Math.max(1, ...(st?.linkTypes ?? []).map((t) => num(t.links))));
  const storeVersions = $derived(total(measure(stats, 'versionsByType')));
  const storeNodes = $derived(total(measure(stats, 'nodesByType')));
  const linkRows = $derived(total(measure(stats, 'linkRowsByType')));
  const linkSets = $derived(measure(stats, 'linkSetsByMode'));

  const tiles = $derived([
    { label: 'Nodes', value: fmt(st?.nodes), hint: num(st?.deleted) ? `${fmt(st?.deleted)} deleted at the head` : 'at the head' },
    { label: 'Links', value: fmt(st?.links), hint: `${fmt(num(st?.links) / Math.max(1, num(st?.nodes)), 2)} per node` },
    { label: 'Node types', value: fmt(st?.types?.length ?? 0), hint: `${fmt(st?.linkTypes?.length ?? 0)} link types` },
    { label: 'Versions', value: fmt(storeVersions), hint: `${fmt(storeVersions / Math.max(1, storeNodes), 2)} per node, all branches` },
    { label: 'Changes', value: fmt(total(measure(stats, 'changesByStatus'))), hint: `${fmt(total(measure(stats, 'baselinesByKind')))} baselines` },
    { label: 'Branches', value: fmt(stats?.branches?.total), hint: `${fmt(stats?.branches?.lasting)} lasting, ${fmt(stats?.branches?.ephemeral)} of changes` },
    { label: 'Connected parts', value: fmt(st?.componentCount), hint: `largest ${fmt(st?.components?.[0])} nodes` },
    { label: 'Isolated', value: fmt(st?.isolated), hint: `${pct(st?.isolated, st?.nodes)} of the nodes` },
    { label: 'Suspect links', value: fmt(st?.suspect), hint: `${pct(st?.suspect, st?.links)} of the links` },
    { label: 'Density', value: `${(num(st?.density) * 100).toPrecision(2)}%`, hint: 'of the possible directed links' },
    { label: 'Reciprocal pairs', value: fmt(st?.reciprocal), hint: `${fmt(st?.selfLinks)} self-link${num(st?.selfLinks) === 1 ? '' : 's'}` },
    { label: 'Owners · projects', value: `${fmt(st?.owners)} · ${fmt(st?.projects)}`, hint: 'distinct units and projects' },
  ]);

  // the sizes of the largest parts, one chip per size with how many parts have it
  const parts = $derived.by(() => {
    const m = new Map<number, number>();
    for (const c of st?.components ?? []) m.set(num(c), (m.get(num(c)) ?? 0) + 1);
    return [...m.entries()];
  });

  const degreeRows = $derived([
    { label: 'Outgoing', d: st?.out },
    { label: 'Incoming', d: st?.in },
    { label: 'Total', d: st?.degree },
    { label: 'Versions', d: st?.versions },
    { label: 'Properties', d: st?.properties },
  ]);
</script>

{#snippet bars(rows: [string, number][], unit: string)}
  {@const top = Math.max(1, ...rows.map((r) => r[1]))}
  {@const whole = rows.reduce((n, r) => n + r[1], 0)}
  <ul class="bars">
    {#each rows as [label, n] (label)}
      <li title="{label}: {fmt(n)} {unit} ({pct(n, whole)})">
        <span class="label">{label}</span>
        <span class="track">{#if n}<span class="fill" style:width="{(100 * n) / top}%"></span>{/if}</span>
        <span class="value">{fmt(n)}</span>
      </li>
    {:else}
      <li class="empty">Nothing yet.</li>
    {/each}
  </ul>
{/snippet}

<div class="editor-page">
  <div class="editor-head">
    <Icon name="chart" size={18} />
    <h2>Statistics · {namespace}</h2>
    <label class="branch"
      >Branch
      <select bind:value={branch} disabled={loading}>
        {#each branches as b (b)}<option value={b}>{b}</option>{/each}
      </select>
    </label>
    <span class="grow"></span>
    {#if stats?.baselineId}<span class="hint">head written {formatDate(stats.baselineAt)}</span>{/if}
    <button type="button" class="ghost small" title="Refresh" aria-label="Refresh" disabled={loading} onclick={() => load(namespace, branch)}
      ><Icon name="refresh" size={14} /></button
    >
  </div>
  {#if error}<div class="alert">{error}</div>{/if}
  {#if stats && !stats.baselineId}
    <p class="empty">Nothing committed on {branch} yet: the state is empty.</p>
  {/if}

  {#if st && num(st.nodes)}
    {#if lines.length}
      <section class="card insights">
        <h3>At a glance</h3>
        <ul>
          {#each lines as l (l)}<li>{l}</li>{/each}
        </ul>
      </section>
    {/if}

    <section class="tiles" aria-label="Key figures">
      {#each tiles as t (t.label)}
        <div class="tile">
          <span class="tlabel">{t.label}</span>
          <span class="tvalue">{t.value}</span>
          <span class="thint">{t.hint}</span>
        </div>
      {/each}
    </section>

    <div class="cols">
      <section class="card">
        <h3>Per node</h3>
        <table>
          <thead>
            <tr><th></th><th class="num">Min</th><th class="num">Median</th><th class="num">Mean</th><th class="num">P90</th><th class="num">Max</th></tr>
          </thead>
          <tbody>
            {#each degreeRows as r (r.label)}
              <tr>
                <td>{r.label === 'Versions' || r.label === 'Properties' ? r.label : `${r.label} links`}</td>
                <td class="num">{fmt(r.d?.min)}</td>
                <td class="num">{fmt(r.d?.median, 1)}</td>
                <td class="num">{fmt(r.d?.mean, 2)}</td>
                <td class="num">{fmt(r.d?.p90)}</td>
                <td class="num">{fmt(r.d?.max)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>
      <section class="card">
        <h3>Nodes by number of links</h3>
        {@render bars((st.histogram ?? []).map((b): [string, number] => [b.label ?? '', num(b.count)]), 'nodes')}
      </section>
    </div>

    <section class="card">
      <h3>Node types</h3>
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th>Type</th><th class="num">Nodes</th><th class="share"></th><th class="num">Out mean · max</th><th class="num">In mean · max</th>
              <th class="num">Versions mean · max</th><th class="num">Props</th><th class="num">Isolated</th><th>States</th>
            </tr>
          </thead>
          <tbody>
            {#each st.types ?? [] as t (t.type)}
              <tr>
                <td class="mono" title={t.type}>{shortType(t.type)}</td>
                <td class="num">{fmt(t.nodes)}</td>
                <td class="share" title={pct(t.nodes, st.nodes)}><span class="track"><span class="fill" style:width="{(100 * num(t.nodes)) / typeMax}%"></span></span></td>
                <td class="num">{fmt(t.out?.mean, 2)} · {fmt(t.out?.max)}</td>
                <td class="num">{fmt(t.in?.mean, 2)} · {fmt(t.in?.max)}</td>
                <td class="num">{fmt(t.versions?.mean, 2)} · {fmt(t.versions?.max)}</td>
                <td class="num">{fmt(t.properties?.mean, 1)}</td>
                <td class="num">{fmt(t.isolated)}</td>
                <td>
                  <ul class="chips">
                    {#each ranked(t.states) as [s, n] (s)}<li class="chip">{s} <span class="n">{fmt(n)}</span></li>{/each}
                  </ul>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <section class="card">
      <h3>Link types</h3>
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th>Type</th><th class="num">Links</th><th class="share"></th><th class="num">Suspect</th><th class="num">Sources</th><th class="num">Targets</th>
              <th class="num">Max per node</th><th>Between</th>
            </tr>
          </thead>
          <tbody>
            {#each st.linkTypes ?? [] as l (l.type)}
              <tr>
                <td class="mono" title={l.type}>{shortType(l.type)}</td>
                <td class="num">{fmt(l.links)}</td>
                <td class="share" title={pct(l.links, st.links)}><span class="track"><span class="fill" style:width="{(100 * num(l.links)) / linkMax}%"></span></span></td>
                <td class="num">{fmt(l.suspect)}</td>
                <td class="num">{fmt(l.sources)}</td>
                <td class="num">{fmt(l.targets)}</td>
                <td class="num">{fmt(l.maxOut)}</td>
                <td class="pairs">
                  {#each (l.pairs ?? []).slice(0, 3) as p (`${p.from}>${p.to}`)}
                    <span>{shortType(p.from)} → {shortType(p.to)} <span class="hint">{fmt(p.count)}</span></span>
                  {/each}
                  {#if (l.pairs?.length ?? 0) > 3}<span class="hint">+{(l.pairs?.length ?? 0) - 3} more</span>{/if}
                </td>
              </tr>
            {:else}
              <tr><td colspan="8" class="empty">No link between the nodes of the state.</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
    </section>

    <div class="cols">
      <section class="card">
        <h3>Most connected nodes</h3>
        <table>
          <thead><tr><th>Node</th><th>Type</th><th class="num">Out</th><th class="num">In</th></tr></thead>
          <tbody>
            {#each st.hubs ?? [] as h (h.id)}
              <tr>
                <td><button type="button" class="link mono" onclick={() => openNode({ id: h.id ?? '' }, { pin: true })}>{h.key}</button></td>
                <td class="mono">{shortType(h.type)}</td>
                <td class="num">{fmt(h.out)}</td>
                <td class="num">{fmt(h.in)}</td>
              </tr>
            {:else}
              <tr><td colspan="4" class="empty">No link yet.</td></tr>
            {/each}
          </tbody>
        </table>
      </section>
      <section class="card">
        <h3>Connected parts</h3>
        <p class="hint">
          {fmt(st.componentCount)} part{num(st.componentCount) === 1 ? '' : 's'}, ignoring the direction of the links; the largest ones, by number of
          nodes:
        </p>
        <ul class="chips">
          {#each parts as [size, n] (size)}<li class="chip" title="{n} part{n === 1 ? '' : 's'} of {size} node{size === 1 ? '' : 's'}">{fmt(size)} node{size === 1 ? '' : 's'} <span class="n">× {n}</span></li>{/each}
        </ul>
        {#if num(st.external)}<p class="hint">{fmt(st.external)} link{num(st.external) === 1 ? '' : 's'} leave the state (another namespace, or a deleted node).</p>{/if}
      </section>
    </div>
  {/if}

  {#if stats && storeNodes}
    <h3 class="section">In the store <span class="hint">every branch, every version of {namespace}</span></h3>
    <div class="cols three">
      <section class="card">
        <h4>Nodes by number of versions</h4>
        {@render bars(versionHistogram(measure(stats, 'versionsPerNode')), 'nodes')}
      </section>
      <section class="card">
        <h4>Versions by reason</h4>
        {@render bars(ranked(measure(stats, 'versionsByReason'), 'edit'), 'versions')}
      </section>
      <section class="card">
        <h4>How versions hold their links</h4>
        {@render bars(ranked(linkSets), 'versions')}
        <p class="hint">
          {fmt(linkRows)} link rows written, {fmt(total(measure(stats, 'linkDrops')))} dropping a link; {pct(num(linkSets.shared), storeVersions, 0)} of the
          versions share the set of an earlier one.
        </p>
      </section>
      <section class="card">
        <h4>Changes by status</h4>
        {@render bars(ranked(measure(stats, 'changesByStatus')), 'changes')}
      </section>
      <section class="card">
        <h4>What the changes committed</h4>
        {@render bars(ranked(measure(stats, 'changeCounters')), 'counted')}
        <p class="hint">Summed over the root changes, net per node: a sub-change is counted in its parent once merged.</p>
      </section>
      <section class="card">
        <h4>Baselines by kind</h4>
        {@render bars(ranked(measure(stats, 'baselinesByKind')), 'baselines')}
      </section>
      <section class="card">
        <h4>Log entries by stream</h4>
        {@render bars(byStream(measure(stats, 'logByType')), 'entries')}
        <p class="hint">
          {fmt(total(measure(stats, 'joins')))} versions joined another branch; {fmt(total(measure(stats, 'tags')))} tags.
        </p>
      </section>
    </div>
  {/if}
</div>

<style>
  .branch {
    align-self: center;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    color: var(--muted);
    font-size: 0.9em;
  }
  .insights ul {
    margin: 0;
    padding-left: 1.2rem;
  }
  .insights li {
    margin: 0.15rem 0;
  }
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .tile {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    padding: 0.5rem 0.65rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .tlabel,
  .thint {
    font-size: 0.78rem;
    color: var(--muted);
  }
  .tvalue {
    font-size: 1.35rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .cols {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
    gap: 0 0.75rem;
  }
  .cols.three {
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  }
  .scroll {
    overflow-x: auto;
  }
  h3.section {
    margin: 1.2rem 0 0.6rem;
  }
  h4 {
    margin: 0 0 0.5rem;
  }
  .bars {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .bars li {
    display: grid;
    grid-template-columns: minmax(70px, 30%) 1fr auto;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.88em;
  }
  .bars li:hover .fill {
    background: var(--text);
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .value {
    font-variant-numeric: tabular-nums;
    color: var(--muted);
  }
  .track {
    display: block;
    height: 10px;
  }
  .fill {
    display: block;
    height: 100%;
    min-width: 2px;
    border-radius: 0 4px 4px 0;
    background: var(--accent);
  }
  td.share,
  th.share {
    width: 120px;
    vertical-align: middle;
  }
  .pairs {
    display: flex;
    flex-wrap: wrap;
    gap: 0.2rem 0.8rem;
    font-size: 0.9em;
  }
</style>
