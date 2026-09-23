// Catalogs shared by the explorers, the tester and search: methodologies
// (all versions), baselines, changes.
import {
  registry,
  graph,
  errorMessage,
  compareVersions,
  type Baseline,
  type ChangeSet,
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
export const changes: Catalog<ChangeSet> = $state(catalog());

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

export function refreshBaselines(): Promise<void> {
  return fill(baselines, async () => (await graph.listBaselines()).baselines ?? []);
}

export function refreshChanges(): Promise<void> {
  return fill(changes, async () => {
    const list = (await graph.listChanges()).changes ?? [];
    return list.sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''));
  });
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
