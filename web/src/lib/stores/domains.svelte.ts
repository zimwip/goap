// Shared domains: the catalog (all versions) and in-memory drafts, one per
// domain@version. Editing a domain involves no change, impact or proposal:
// it is saved and published straight to the registry.
import { SvelteMap } from 'svelte/reactivity';
import { registry, errorMessage, bumpPatch, compareVersions, type Domain, type DomainSummary, type DomainUser, type Issue } from '../api';
import { emptyDomainForm, fromDomainForm, toDomainForm, type DomainForm } from '../domainForm';
import { normalizePath } from '../methodologyForm';
import type { NormIssue } from './drafts.svelte';

interface Catalog {
  items: DomainSummary[];
  loading: boolean;
  loaded: boolean;
  error: string;
}

export const domains: Catalog = $state({ items: [], loading: false, loaded: false, error: '' });

export async function refreshDomains(): Promise<void> {
  domains.loading = true;
  try {
    domains.items = (await registry.listDomains(true)).domains ?? [];
    domains.error = '';
  } catch (e) {
    domains.error = errorMessage(e);
  } finally {
    domains.loading = false;
    domains.loaded = true;
  }
}

export interface DomainGroup {
  name: string;
  description: string;
  versions: DomainSummary[];
}

/** Versions grouped by name, from most recent to oldest. */
export function groupedDomains(): DomainGroup[] {
  const byName = new Map<string, DomainSummary[]>();
  for (const d of domains.items) byName.set(d.name ?? '', [...(byName.get(d.name ?? '') ?? []), d]);
  const out: DomainGroup[] = [];
  for (const [name, versions] of byName) {
    versions.sort((a, b) => compareVersions(b.version, a.version));
    const ref = versions.find((v) => v.status === 'published') ?? versions[0];
    out.push({ name, description: ref?.description ?? '', versions });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** Published versions of every domain, for pickers (most recent first). */
export function publishedDomainVersions(name: string): DomainSummary[] {
  return domains.items
    .filter((d) => d.name === name && d.status === 'published')
    .sort((a, b) => compareVersions(b.version, a.version));
}

export function domainKey(name: string, version: string): string {
  return `${name}@${version}`;
}

function under(issuePath: string, path: string): boolean {
  return issuePath === path || issuePath.startsWith(`${path}.`) || issuePath.startsWith(`${path}[`);
}

export class DomainDraft {
  readonly name: string;
  readonly version: string;
  readonly isNew: boolean;
  readonly key: string;

  form = $state<DomainForm>(emptyDomainForm());
  snapshot = $state(JSON.stringify(emptyDomainForm()));
  status = $state('draft');
  meta = $state<Pick<Domain, 'createdAt' | 'updatedAt' | 'publishedAt' | 'updatedBy'>>({});
  loading = $state(true);
  loadError = $state('');
  busy = $state('');
  error = $state('');
  /** null: not yet validated */
  issues = $state<Issue[] | null>(null);
  validatedAt = $state('');
  /** methodology versions referencing this version */
  usage = $state<DomainUser[]>([]);

  private loaded: Promise<void> | undefined;

  constructor(name: string, version: string) {
    this.name = name;
    this.version = version;
    this.isNew = !name;
    this.key = this.isNew ? 'new' : domainKey(name, version);
    if (this.isNew) {
      this.snapshot = JSON.stringify(this.form);
      this.loading = false;
    }
  }

  readonly current = $derived(JSON.stringify(this.form));
  readonly dirty = $derived(this.current !== this.snapshot);
  readonly readonly = $derived(this.status !== 'draft');
  readonly allIssues = $derived<NormIssue[]>((this.issues ?? []).map((i) => ({ ...i, norm: normalizePath(i.path) })));
  readonly nodeTypeNames = $derived([...new Set(this.form.nodeTypes.map((n) => n.name.trim()).filter(Boolean))]);
  get canPublish(): boolean {
    return !this.readonly && !this.isNew && !this.dirty && !this.busy && this.issues !== null && this.allIssues.length === 0 && this.validatedAt === this.current;
  }
  get label(): string {
    return this.isNew ? 'New domain' : `${this.name} v${this.version}`;
  }

  bad = (path: string, exact = false): boolean => this.allIssues.some((i) => (exact ? i.norm === path : under(i.norm, path)));
  count(path: string): number {
    return this.allIssues.filter((i) => under(i.norm, path)).length;
  }

  private apply(d: Domain) {
    this.form = toDomainForm(d);
    this.snapshot = JSON.stringify(this.form);
    this.status = d.status || 'draft';
    this.meta = { createdAt: d.createdAt, updatedAt: d.updatedAt, publishedAt: d.publishedAt, updatedBy: d.updatedBy };
  }

  ensureLoaded(): Promise<void> {
    if (this.isNew) return Promise.resolve();
    this.loaded ??= this.reload();
    return this.loaded;
  }

  async reload(): Promise<void> {
    this.loading = true;
    this.loadError = '';
    this.issues = null;
    try {
      const res = await registry.getDomain(this.name, this.version);
      if (!res.domain) throw new Error('Domain not found.');
      this.apply(res.domain);
      this.loading = false;
      if (this.status === 'draft') await this.validate(true);
      await this.loadUsage();
    } catch (e) {
      this.loadError = errorMessage(e);
    } finally {
      this.loading = false;
    }
  }

  async loadUsage(): Promise<void> {
    if (this.isNew) return;
    try {
      this.usage = (await registry.getDomainUsage(this.name, this.version)).methodologies ?? [];
    } catch {
      this.usage = [];
    }
  }

  revert(): void {
    this.form = JSON.parse(this.snapshot) as DomainForm;
  }

  private async run<T>(what: string, fn: () => Promise<T>): Promise<T | undefined> {
    this.busy = what;
    this.error = '';
    try {
      return await fn();
    } catch (e) {
      this.error = errorMessage(e);
      return undefined;
    } finally {
      this.busy = '';
    }
  }

  async validate(quiet = false): Promise<void> {
    if (this.readonly) return;
    const at = this.current;
    const d = fromDomainForm(this.form);
    if (quiet) {
      try {
        const res = await registry.validateDomain(d);
        if (this.current === at) {
          this.issues = res.issues ?? [];
          this.validatedAt = at;
        }
      } catch {
        // background validation: network errors are ignored
      }
      return;
    }
    await this.run('validate', async () => {
      this.issues = (await registry.validateDomain(d)).issues ?? [];
      this.validatedAt = at;
    });
  }

  async save(): Promise<Domain | undefined> {
    if (this.readonly) return undefined;
    if (!this.form.name.trim() || !this.form.version.trim()) {
      this.error = 'Name and version are required.';
      return undefined;
    }
    const at = this.current;
    const res = await this.run('save', () => registry.saveDomain(fromDomainForm(this.form)));
    if (!res) return undefined;
    const saved = res.domain ?? fromDomainForm(this.form);
    this.snapshot = at;
    this.status = saved.status || 'draft';
    this.meta = { createdAt: saved.createdAt, updatedAt: saved.updatedAt, publishedAt: saved.publishedAt, updatedBy: saved.updatedBy };
    this.issues = res.issues ?? [];
    this.validatedAt = at;
    void refreshDomains();
    return saved;
  }

  async publish(): Promise<boolean> {
    if (!confirm(`Publish ${this.label}? The version will become immutable and usable by methodologies.`)) return false;
    const res = await this.run('publish', () => registry.publishDomain(this.name, this.version));
    if (!res) return false;
    this.status = res.domain?.status || 'published';
    if (res.domain?.publishedAt) this.meta = { ...this.meta, publishedAt: res.domain.publishedAt };
    void refreshDomains();
    return true;
  }

  async newVersion(): Promise<string | undefined> {
    if (this.dirty && !confirm('There are unsaved changes: the new version is copied from the saved version. Continue?')) return undefined;
    const v = prompt(`Number of the new version (copy of v${this.version}):`, bumpPatch(this.version))?.trim();
    if (!v) return undefined;
    const res = await this.run('version', () => registry.createDomainVersion(this.name, this.version, v));
    if (!res) return undefined;
    void refreshDomains();
    return res.domain?.version ?? v;
  }

  async exportYaml(): Promise<void> {
    await this.run('export', async () => {
      const res = await registry.exportDomain(this.name, this.version);
      const url = URL.createObjectURL(new Blob([res.yaml ?? ''], { type: 'application/yaml' }));
      const a = document.createElement('a');
      a.href = url;
      a.download = res.filename || `domain-${this.name}-${this.version}.yaml`;
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  }

  /** Deletes a draft or archives a published version. Returns "deleted" / "archived". */
  async remove(): Promise<'deleted' | 'archived' | undefined> {
    const draft = this.status === 'draft';
    const msg = draft ? `Permanently delete the draft ${this.label}?` : `Archive ${this.label}? Methodologies can no longer reference it.`;
    if (!confirm(msg)) return undefined;
    const ok = await this.run('delete', async () => {
      await registry.deleteDomain(this.name, this.version);
      return true;
    });
    if (!ok) return undefined;
    void refreshDomains();
    if (draft) {
      domainDrafts.delete(this.key);
      return 'deleted';
    }
    await this.reload();
    return 'archived';
  }
}

export const domainDrafts = new SvelteMap<string, DomainDraft>();

/** Draft of a domain version (created and loaded on demand). */
export function getDomainDraft(name: string, version: string): DomainDraft {
  const key = name ? domainKey(name, version) : 'new';
  let d = domainDrafts.get(key);
  if (!d) {
    let created: DomainDraft | undefined;
    $effect.root(() => {
      created = new DomainDraft(name, version);
    });
    d = created!;
    domainDrafts.set(key, d);
    void d.ensureLoaded();
  }
  return d;
}

export function peekDomainDraft(key: string): DomainDraft | undefined {
  return domainDrafts.get(key);
}
