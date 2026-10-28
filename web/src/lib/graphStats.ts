// The statistics of the graph (graph statistics tool): pure helpers over the answers of GetGraphOverview and
// GetGraphStats, read by the left-bar explorer and the statistics tab.
import type { Distribution, GraphStats, NamespaceSummary } from './api';

/** A number of the wire: a double may come as "NaN" / "Infinity". */
export function num(x: number | string | undefined): number {
  const n = typeof x === 'number' ? x : Number(x ?? 0);
  return Number.isFinite(n) ? n : 0;
}

/** A count, grouped by thousands; a mean with one decimal at most. */
export function fmt(x: number | string | undefined, decimals = 0): string {
  const n = num(x);
  return n.toLocaleString('en-US', { maximumFractionDigits: decimals, minimumFractionDigits: 0 });
}

/** A share in percent, "–" when the whole is empty. */
export function pct(part: number | undefined, whole: number | undefined, decimals = 1): string {
  const w = num(whole);
  if (!w) return '–';
  return `${(100 * num(part) / w).toFixed(decimals)}%`;
}

/** The counts of a measure of the store (StoreMeasure.name), by key. */
export function measure(s: GraphStats | undefined, name: string): Record<string, number> {
  return s?.store?.find((m) => m.name === name)?.counts ?? {};
}

/** The sum of the counts of a measure. */
export function total(counts: Record<string, number> | undefined): number {
  let n = 0;
  for (const v of Object.values(counts ?? {})) n += num(v);
  return n;
}

/** The entries of counts, the largest first, then by key; an empty key gets the label given. */
export function ranked(counts: Record<string, number> | undefined, empty = '(none)'): [string, number][] {
  return Object.entries(counts ?? {})
    .map(([k, v]): [string, number] => [k === '' ? empty : k, num(v)])
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** The entries of the log by stream: `fact.artifact` and `fact.risk` count under `fact`. */
export function byStream(counts: Record<string, number> | undefined): [string, number][] {
  const out: Record<string, number> = {};
  for (const [k, v] of Object.entries(counts ?? {})) {
    const stream = k.split('.')[0] || k;
    out[stream] = (out[stream] ?? 0) + num(v);
  }
  return ranked(out);
}

/** The histogram of the versions per node, by number of versions (numeric keys in order, the tail folded from `fold`). */
export function versionHistogram(counts: Record<string, number> | undefined, fold = 10): [string, number][] {
  const rows = new Map<number, number>();
  let tail = 0;
  for (const [k, v] of Object.entries(counts ?? {})) {
    const n = Number(k);
    if (n >= fold) tail += num(v);
    else rows.set(n, (rows.get(n) ?? 0) + num(v));
  }
  const out: [string, number][] = [...rows.entries()].sort((a, b) => a[0] - b[0]).map(([n, c]) => [String(n), c]);
  if (tail) out.push([`${fold}+`, tail]);
  return out;
}

/** The short name of a qualified type: `alm@Requirement` → `Requirement`. */
export function shortType(t: string | undefined): string {
  const s = t ?? '';
  const i = s.indexOf('@');
  return i >= 0 ? s.slice(i + 1) : s;
}

/** One line of a distribution: min · median · mean · p90 · max. */
export function spread(d: Distribution | undefined): string {
  if (!d) return '–';
  return `${fmt(d.min)} · ${fmt(d.median, 1)} · ${fmt(d.mean, 2)} · ${fmt(d.p90)} · ${fmt(d.max)}`;
}

/** The totals of the overview, every namespace added up. */
export function overviewTotals(ns: NamespaceSummary[]): Required<Omit<NamespaceSummary, 'namespace' | 'types' | 'source'>> {
  const t = { nodes: 0, versions: 0, linkRows: 0, changes: 0, commits: 0, branches: 0, logEntries: 0 };
  for (const n of ns) {
    t.nodes += num(n.nodes);
    t.versions += num(n.versions);
    t.linkRows += num(n.linkRows);
    t.changes += num(n.changes);
    t.commits += num(n.commits);
    t.branches += num(n.branches);
    t.logEntries += num(n.logEntries);
  }
  return t;
}

/** What a reader learns at a glance from the shape of a state: a few sentences, the most telling first. */
export function insights(s: GraphStats | undefined): string[] {
  const st = s?.state;
  if (!st || !num(st.nodes)) return [];
  const out: string[] = [];
  const nodes = num(st.nodes);
  const links = num(st.links);
  const largest = num(st.components?.[0]);
  if (num(st.componentCount) === 1) out.push('Every node is reachable from every other one, ignoring the direction of the links.');
  else if (largest) out.push(`The largest connected part holds ${pct(largest, nodes)} of the nodes, across ${fmt(st.componentCount)} parts.`);
  if (num(st.isolated)) out.push(`${fmt(st.isolated)} node${num(st.isolated) > 1 ? 's have' : ' has'} no link at all (${pct(st.isolated, nodes)}).`);
  if (num(st.suspect)) out.push(`${fmt(st.suspect)} link${num(st.suspect) > 1 ? 's point' : ' points'} to an older version of their target (${pct(st.suspect, links)} of the links): suspect, worth a review.`);
  const hub = st.hubs?.[0];
  if (hub && links) out.push(`${hub.key} (${shortType(hub.type)}) is the most connected node: ${fmt(num(hub.out) + num(hub.in))} links.`);
  const top = st.types?.[0];
  if (top && (st.types?.length ?? 0) > 1) out.push(`${shortType(top.type)} is the most common type: ${pct(top.nodes, nodes)} of the nodes.`);
  if (num(st.external)) out.push(`${fmt(st.external)} link${num(st.external) > 1 ? 's leave' : ' leaves'} the namespace (or reach a deleted node).`);
  const versions = st.versions;
  if (versions && num(versions.max) > 1) out.push(`Nodes were written ${fmt(versions.mean, 2)} times on average, ${fmt(versions.max)} at most.`);
  return out;
}
