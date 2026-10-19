// Catalogs shared by the explorers, the tester and search: methodologies
// (all versions), baselines, changes.
import { SvelteMap } from 'svelte/reactivity';
import { isMeta } from './session.svelte';
import {
  registry,
  graph,
  errorMessage,
  compareVersions,
  type Baseline,
  type Change,
  type Methodology,
  type MethodologySummary,
} from '../api';

interface Catalog<T> {
  items: T[];
  loading: boolean;
  loaded: boolean;
  error: string;
}

function catalog<T>(): Catalog<T> {
  return { items: [], loading: false, loaded: false, error: '' };
}

export const methodologies: Catalog<MethodologySummary> = $state(catalog());
export const baselines: Catalog<Baseline> = $state(catalog());
export const changes: Catalog<Change> = $state(catalog());

async function fill<T>(c: Catalog<T>, fn: () => Promise<T[]>): Promise<void> {
  c.loading = true;
  try {
    c.items = await fn();
    c.error = '';
  } catch (e) {
    c.error = errorMessage(e);
  } finally {
    c.loading = false;
    c.loaded = true;
  }
}

export function refreshMethodologies(): Promise<void> {
  return fill(methodologies, async () => (await registry.listMethodologies(true)).methodologies ?? []);
}

let baselinesFor = '';

/** The namespace the `baselines` catalog currently holds. */
export function baselinesNamespace(): string {
  return baselinesFor;
}

/** Baselines are namespace-scoped: this replaces the store's contents with that namespace's baselines. */
export function refreshBaselines(namespace: string): Promise<void> {
  baselinesFor = namespace;
  return fill(baselines, async () => (await graph.listBaselines(namespace)).baselines ?? []);
}

/** The baselines of every namespace holding nodes (newest first in each), the meta-domain methodology aside. */
export async function baselinesByNamespace(signal?: AbortSignal): Promise<{ namespace: string; baselines: Baseline[] }[]> {
  const nss = ((await graph.listNamespaces(signal)).namespaces ?? []).filter((n) => !isMeta(n));
  return Promise.all(
    nss.map(async (namespace) => ({ namespace, baselines: [...((await graph.listBaselines(namespace, signal)).baselines ?? [])].reverse() })),
  );
}

/** The statuses of the changes that are over: their number only grows, so they are listed by pages, newest first;
 * the open changes are listed whole. */
export const CLOSED_STATUSES = ['applied', 'abandoned'] as const;
export type ClosedStatus = (typeof CLOSED_STATUSES)[number];
const OPEN_STATUSES = ['draft', 'active', 'committed'];
const CHANGES_PAGE = 50;

interface ChangePages {
  /** the changes of the pages read, newest first */
  items: Change[];
  /** the cursor of the next page ('': none) */
  next: string;
  /** the number of changes of the scope, every page together */
  total: number;
  loading: boolean;
}

/** The pages read of the changes that are over, by status. */
export const closedChanges: Record<ClosedStatus, ChangePages> = $state({
  applied: { items: [], next: '', total: 0, loading: false },
  abandoned: { items: [], next: '', total: 0, loading: false },
});

// what the pages of the changes that are over hold: the changes of a project ('': every project) matching a text
let changeScope = { project: '', query: '' };

const byNewest = (a: Change, b: Change) => (b.createdAt ?? '').localeCompare(a.createdAt ?? '');

function listed(open: Change[]): Change[] {
  return [...open, ...CLOSED_STATUSES.flatMap((s) => closedChanges[s].items)].sort(byNewest);
}

let openChanges: Change[] = [];

// the first pages of the changes that are over, as many as are shown already (a refresh keeps what was loaded more)
async function readClosed(s: ClosedStatus, keep: boolean): Promise<void> {
  const scope = changeScope;
  const limit = keep ? Math.max(CHANGES_PAGE, closedChanges[s].items.length) : CHANGES_PAGE;
  // the heads only: the catalog names and filters the changes, a change tab reads its change whole
  const r = await graph.listChanges(undefined, { headerOnly: true, status: [s], ...scope, limit });
  if (scope !== changeScope) return; // the scope moved meanwhile: its own read answers
  closedChanges[s].items = r.changes ?? [];
  closedChanges[s].next = r.nextCursor ?? '';
  closedChanges[s].total = r.total ?? 0;
}

/** Reads the open changes again, and the pages of the changes that are over already read. */
export function refreshChanges(): Promise<void> {
  return fill(changes, async () => {
    const [open] = await Promise.all([
      graph.listChanges(undefined, { headerOnly: true, status: OPEN_STATUSES }),
      ...CLOSED_STATUSES.map((s) => readClosed(s, true)),
    ]);
    openChanges = open.changes ?? [];
    return listed(openChanges);
  });
}

/** Narrows the pages of the changes that are over to a project ('': every project) and a text, from their first page. */
export async function setChangesScope(project: string, query: string): Promise<void> {
  query = query.trim();
  if (project === changeScope.project && query === changeScope.query) return;
  changeScope = { project, query };
  if (!changes.loaded) return;
  try {
    await Promise.all(CLOSED_STATUSES.map((s) => readClosed(s, false)));
    changes.items = listed(openChanges);
    changes.error = '';
  } catch (e) {
    changes.error = errorMessage(e);
  }
}

/** Reads the next page of the changes of a status that are over. */
export async function loadMoreChanges(s: ClosedStatus): Promise<void> {
  const p = closedChanges[s];
  if (!p.next || p.loading) return;
  const scope = changeScope;
  p.loading = true;
  try {
    const r = await graph.listChanges(undefined, { headerOnly: true, status: [s], ...scope, limit: CHANGES_PAGE, cursor: p.next });
    if (scope !== changeScope) return;
    const seen = new Set(p.items.map((c) => c.id));
    p.items = [...p.items, ...(r.changes ?? []).filter((c) => !seen.has(c.id))];
    p.next = r.nextCursor ?? '';
    p.total = r.total ?? p.total;
    changes.items = listed(openChanges);
    changes.error = '';
  } catch (e) {
    changes.error = errorMessage(e);
  } finally {
    p.loading = false;
  }
}

export interface MethodologyGroup {
  name: string;
  description: string;
  versions: MethodologySummary[];
}

/** Versions grouped by name, from most recent to oldest. */
export function groupedMethodologies(): MethodologyGroup[] {
  const byName = new Map<string, MethodologySummary[]>();
  for (const m of methodologies.items) {
    const k = m.name ?? '';
    byName.set(k, [...(byName.get(k) ?? []), m]);
  }
  const out: MethodologyGroup[] = [];
  for (const [name, versions] of byName) {
    versions.sort((a, b) => compareVersions(b.version, a.version));
    const ref = versions.find((v) => v.status === 'published') ?? versions[0];
    out.push({ name, description: ref?.description ?? '', versions });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** Only published versions are executable: the most recent one per name. */
export function latestPublished(): MethodologySummary[] {
  const best = new Map<string, MethodologySummary>();
  for (const m of methodologies.items) {
    if (m.status !== 'published' || !m.name) continue;
    const cur = best.get(m.name);
    if (!cur || compareVersions(m.version, cur.version) > 0) best.set(m.name, m);
  }
  return [...best.values()].sort((a, b) => (a.name ?? '').localeCompare(b.name ?? ''));
}

/** Full definition of the latest published version of each methodology, by name (read on demand). */
export const published = new SvelteMap<string, Methodology>();
const publishedPending = new Map<string, Promise<void>>();

export function loadMethodology(name: string): Promise<void> {
  if (!name || published.has(name)) return Promise.resolve();
  let p = publishedPending.get(name);
  if (!p) {
    p = registry
      .getMethodology(name, '')
      .then((r) => {
        if (r.methodology) published.set(name, r.methodology);
      })
      .catch(() => undefined)
      .finally(() => publishedPending.delete(name));
    publishedPending.set(name, p);
  }
  return p;
}
