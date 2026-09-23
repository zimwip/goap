// In-memory drafts: one per methodology@version, shared by every tab
// editing a part of it (methodology, agent, action…). Saving from any of
// these tabs saves the whole methodology.
import { SvelteMap } from 'svelte/reactivity';
import { registry, errorMessage, bumpPatch, type Issue, type Methodology } from '../api';
import {
  emptyForm,
  toForm,
  fromForm,
  normalizePath,
  conditionNames,
  renameReferences,
  type MethodologyForm,
  type Section,
  type SectionItem,
} from '../methodologyForm';
import { refreshMethodologies } from './catalog.svelte';

export interface NormIssue extends Issue {
  norm: string;
}

type Meta = Pick<Methodology, 'createdAt' | 'updatedAt' | 'publishedAt' | 'updatedBy'>;

export function draftKey(name: string, version: string): string {
  return `${name}@${version}`;
}

function under(issuePath: string, path: string): boolean {
  return issuePath === path || issuePath.startsWith(`${path}.`) || issuePath.startsWith(`${path}[`);
}

export class Draft {
  readonly name: string;
  readonly version: string;
  /** new methodology never saved */
  readonly isNew: boolean;

  form = $state<MethodologyForm>(emptyForm());
  snapshot = $state(JSON.stringify(emptyForm()));
  status = $state('draft');
  meta = $state<Meta>({});
  loading = $state(true);
  loadError = $state('');
  /** operation in progress (save, validate, publish…) */
  busy = $state('');
  error = $state('');
  /** null: not yet validated */
  issues = $state<Issue[] | null>(null);
  localIssues = $state<Issue[]>([]);
  /** form JSON at the last validation */
  validatedAt = $state('');

  readonly key: string;
  private loaded: Promise<void> | undefined;

  constructor(name: string, version: string) {
    this.name = name;
    this.version = version;
    this.isNew = !name;
    this.key = this.isNew ? 'new' : draftKey(name, version);
    if (this.isNew) {
      this.form.version = '0.1.0';
      this.snapshot = JSON.stringify(this.form);
      this.loading = false;
    }
  }

  readonly current = $derived(JSON.stringify(this.form));
  readonly dirty = $derived(this.current !== this.snapshot);
  readonly readonly = $derived(this.status !== 'draft');
  private readonly saved = $derived(JSON.parse(this.snapshot) as MethodologyForm);
  readonly allIssues = $derived<NormIssue[]>(
    [...this.localIssues, ...(this.issues ?? [])].map((i) => ({ ...i, norm: normalizePath(i.path) })),
  );
  readonly conditionOptions = $derived(conditionNames(this.form));
  readonly nodeTypeNames = $derived([...new Set(this.form.nodeTypes.map((n) => n.name.trim()).filter(Boolean))]);
  readonly linkTypeNames = $derived([...new Set(this.form.linkTypes.map((l) => l.name.trim()).filter(Boolean))]);
  get canPublish(): boolean {
    return (
      !this.readonly &&
      !this.isNew &&
      !this.dirty &&
      !this.busy &&
      this.issues !== null &&
      this.allIssues.length === 0 &&
      this.validatedAt === this.current
    );
  }

  get label(): string {
    return this.isNew ? 'New methodology' : `${this.name} v${this.version}`;
  }

  // --- loading --------------------------------------------------------------

  private apply(m: Methodology) {
    this.form = toForm(m);
    this.snapshot = JSON.stringify(this.form);
    this.status = m.status || 'draft';
    this.meta = { createdAt: m.createdAt, updatedAt: m.updatedAt, publishedAt: m.publishedAt, updatedBy: m.updatedBy };
  }

  /** Loads once (concurrent calls share the result). */
  ensureLoaded(): Promise<void> {
    if (this.isNew) return Promise.resolve();
    this.loaded ??= this.reload();
    return this.loaded;
  }

  async reload(): Promise<void> {
    this.loading = true;
    this.loadError = '';
    this.issues = null;
    this.localIssues = [];
    try {
      const res = await registry.getMethodology(this.name, this.version);
      if (!res.methodology) throw new Error('Methodology not found.');
      this.apply(res.methodology);
      this.loading = false;
      if (this.status === 'draft') await this.validate(true);
    } catch (e) {
      this.loadError = errorMessage(e);
    } finally {
      this.loading = false;
    }
  }

  /** Discards changes (back to the last saved state). */
  revert(): void {
    this.form = JSON.parse(this.snapshot) as MethodologyForm;
    this.localIssues = [];
  }

  // --- issues -----------------------------------------------------------------

  /** Does field `path` (or, unless `exact`, one of its descendants) have an issue? */
  bad = (path: string, exact = false): boolean =>
    this.allIssues.some((i) => (exact ? i.norm === path : under(i.norm, path)));

  count(path: string): number {
    return this.allIssues.filter((i) => under(i.norm, path)).length;
  }

  // --- elements ------------------------------------------------------------------

  items(section: Section): SectionItem[] {
    return this.form[section];
  }

  indexOf(section: Section, uid: string, name = ''): number {
    const list = this.items(section);
    let i = list.findIndex((x) => x.uid === uid);
    if (i < 0 && name) i = list.findIndex((x) => x.name === name);
    return i;
  }

  /** Does the element differ from its saved version? */
  itemDirty(section: Section, uid: string): boolean {
    const cur = this.items(section).find((x) => x.uid === uid);
    const old = (this.saved[section] as SectionItem[]).find((x) => x.uid === uid);
    return JSON.stringify(cur) !== JSON.stringify(old);
  }

  rename(section: Section, from: string, to: string): void {
    renameReferences(this.form, section, from.trim(), to.trim());
  }

  // --- operations ------------------------------------------------------------------

  /** Builds the message; returns null if the form has local errors. */
  private build(): Methodology | null {
    const { methodology, issues } = fromForm(this.form);
    this.localIssues = issues;
    if (issues.length) {
      this.error = 'Fix the reported errors before continuing.';
      return null;
    }
    return methodology;
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

  /** Server-side validation; `quiet`: automatic validation (no "busy" state). */
  async validate(quiet = false): Promise<void> {
    if (this.readonly) return;
    const at = this.current;
    const { methodology, issues } = fromForm(this.form);
    this.localIssues = issues;
    if (issues.length) {
      if (!quiet) this.error = 'Fix the reported errors before continuing.';
      return;
    }
    if (quiet) {
      try {
        const res = await registry.validateMethodology(methodology);
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
      this.issues = (await registry.validateMethodology(methodology)).issues ?? [];
      this.validatedAt = at;
    });
  }

  /** Saves the draft. Returns the saved methodology (or undefined). */
  async save(): Promise<Methodology | undefined> {
    if (this.readonly) return undefined;
    if (!this.form.name.trim() || !this.form.version.trim()) {
      this.error = 'Name and version are required.';
      return undefined;
    }
    const m = this.build();
    if (!m) return undefined;
    const at = this.current;
    const res = await this.run('save', () => registry.saveMethodology(m));
    if (!res) return undefined;
    const saved = res.methodology ?? m;
    // The form is kept as is (tabs' local ids); only the metadata comes
    // from the server.
    this.snapshot = at;
    this.status = saved.status || 'draft';
    this.meta = {
      createdAt: saved.createdAt,
      updatedAt: saved.updatedAt,
      publishedAt: saved.publishedAt,
      updatedBy: saved.updatedBy,
    };
    this.issues = res.issues ?? [];
    this.validatedAt = at;
    void refreshMethodologies();
    if (!this.isNew) await this.validate(true);
    return saved;
  }

  async publish(): Promise<boolean> {
    if (!confirm(`Publish ${this.label}? The version will become immutable and executable by the engine.`)) return false;
    const res = await this.run('publish', () => registry.publishMethodology(this.name, this.version));
    if (!res) return false;
    this.status = res.methodology?.status || 'published';
    if (res.methodology?.publishedAt) this.meta = { ...this.meta, publishedAt: res.methodology.publishedAt };
    void refreshMethodologies();
    return true;
  }

  /** Creates a new version (copy of the saved version); returns its number. */
  async newVersion(): Promise<string | undefined> {
    if (this.dirty && !confirm('There are unsaved changes: the new version is copied from the saved version. Continue?'))
      return undefined;
    const v = prompt(`Number of the new version (copy of v${this.version}):`, bumpPatch(this.version))?.trim();
    if (!v) return undefined;
    const res = await this.run('version', () => registry.createVersion(this.name, this.version, v));
    if (!res) return undefined;
    void refreshMethodologies();
    return res.methodology?.version ?? v;
  }

  async exportYaml(): Promise<void> {
    await this.run('export', async () => {
      const res = await registry.exportMethodology(this.name, this.version);
      const blob = new Blob([res.yaml ?? ''], { type: 'application/yaml' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = res.filename || `${this.name}-${this.version}.yaml`;
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  }

  /** Deletes a draft or archives a published version. Returns "deleted" / "archived". */
  async remove(): Promise<'deleted' | 'archived' | undefined> {
    const draft = this.status === 'draft';
    const msg = draft
      ? `Permanently delete the draft ${this.label}?`
      : `Archive ${this.label}? It will no longer be usable to start a process.`;
    if (!confirm(msg)) return undefined;
    const ok = await this.run('delete', async () => {
      await registry.deleteMethodology(this.name, this.version);
      return true;
    });
    if (!ok) return undefined;
    void refreshMethodologies();
    if (draft) {
      drafts.delete(this.key);
      return 'deleted';
    }
    await this.reload();
    return 'archived';
  }
}

export const drafts = new SvelteMap<string, Draft>();

/** Draft of a version (created and loaded on demand). */
export function getDraft(name: string, version: string): Draft {
  const key = name ? draftKey(name, version) : 'new';
  let d = drafts.get(key);
  if (!d) {
    // The draft's $derived must not belong to the effect (component) that
    // creates it: they live as long as the draft.
    let created: Draft | undefined;
    $effect.root(() => {
      created = new Draft(name, version);
    });
    d = created!;
    drafts.set(key, d);
    void d.ensureLoaded();
  }
  return d;
}

/** Draft already in memory (no loading). */
export function peekDraft(key: string): Draft | undefined {
  return drafts.get(key);
}

export function anyDirty(): boolean {
  for (const d of drafts.values()) if (d.dirty) return true;
  return false;
}
