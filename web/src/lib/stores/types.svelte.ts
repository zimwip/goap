// The type catalogue in force (ADR 0012): the resolved node and link types of the published domains and of the
// built-in domains, served by the registry. A type is referenced as "<namespace>@<name>". Loaded once, reloaded
// after a domain is published from the IDE.
import { registry, type AttributeInfo, type LinkTypeInfo, type Lifecycle, type TypeInfo } from '../api';

export const TYPE_SEP = '@';

/** Splits "<namespace>@<name>" ("" namespace for a bare name). */
export function splitType(ref: string | undefined): { namespace: string; name: string } {
  const s = ref ?? '';
  const i = s.indexOf(TYPE_SEP);
  return i < 0 ? { namespace: '', name: s } : { namespace: s.slice(0, i), name: s.slice(i + 1) };
}

/** The name of a type without its namespace (for display). */
export const typeName = (ref: string | undefined): string => splitType(ref).name;

export class TypeCatalog {
  readonly types: Map<string, TypeInfo>;
  readonly links: LinkTypeInfo[];
  /** namespace (domain) → version in force */
  readonly domains: Record<string, string>;

  constructor(types: TypeInfo[] = [], links: LinkTypeInfo[] = [], domains: Record<string, string> = {}) {
    this.types = new Map(types.filter((t) => t.ref).map((t) => [t.ref ?? '', t]));
    this.links = links;
    this.domains = domains;
  }

  type(ref: string | undefined): TypeInfo | undefined {
    return this.types.get(ref ?? '');
  }

  lifecycle(ref: string | undefined): Lifecycle | undefined {
    return this.type(ref)?.lifecycle ?? undefined;
  }

  /** Attributes of a type, its ancestors' first. */
  attributes(ref: string | undefined): AttributeInfo[] {
    return this.type(ref)?.attributes ?? [];
  }

  /** Names of the properties declared by a type, its ancestors' first. */
  properties(ref: string | undefined): string[] {
    return this.attributes(ref).map((a) => a.attribute?.name ?? '').filter(Boolean);
  }

  /** The IDE editor its nodes open in ('' : the default node editor). */
  editor(ref: string | undefined): string {
    return this.type(ref)?.editor ?? '';
  }

  /** The type and its ancestors. */
  typesOf(ref: string | undefined): string[] {
    const t = this.type(ref);
    return t ? [t.ref ?? '', ...(t.ancestors ?? [])] : [];
  }

  /** Qualified node types, of a namespace when given. */
  names(namespace = ''): string[] {
    return [...this.types.keys()].filter((r) => !namespace || splitType(r).namespace === namespace).sort();
  }

  /** Qualified link types, of a namespace when given. */
  linkNames(namespace = ''): string[] {
    return this.links
      .map((l) => l.ref ?? '')
      .filter((r) => r && (!namespace || splitType(r).namespace === namespace))
      .sort();
  }

  /** Link types a node of the type can have as outgoing links (an empty end accepts any node type). */
  linksFrom(ref: string | undefined): LinkTypeInfo[] {
    const types = this.typesOf(ref);
    return this.links.filter((l) => l.ref && (!l.from || types.includes(l.from))).sort((a, b) => (a.ref ?? '').localeCompare(b.ref ?? ''));
  }

  /** Can a link of this type point to a node of the type? */
  linkAccepts(link: LinkTypeInfo, to: string | undefined): boolean {
    return !link.to || this.typesOf(to).includes(link.to);
  }

  /** The composition links (flagged `compose` in their domain): the target is a part of the source. */
  composeLinks(): LinkTypeInfo[] {
    return this.links.filter((l) => l.ref && l.compose);
  }

  /** The type and its subtypes (a link's `to` accepts them all). */
  withSubtypes(ref: string): string[] {
    return [ref, ...[...this.types.values()].filter((t) => t.ancestors?.includes(ref)).map((t) => t.ref ?? '')].filter(Boolean);
  }

  /**
   * The node types a node of the type is composed of: the targets of the composition links its type (or an
   * ancestor) can start, with their subtypes; a link with no `to` composes the types of its source's namespace.
   */
  partsOf(ref: string | undefined): string[] {
    const from = this.typesOf(ref);
    const out = new Set<string>();
    for (const l of this.composeLinks()) {
      if (l.from && !from.includes(l.from)) continue;
      const targets = l.to ? this.withSubtypes(l.to) : this.names(splitType(ref).namespace);
      for (const t of targets) if (t !== ref) out.add(t);
    }
    return [...out].sort();
  }

  /** Does a node of the type have parts? */
  isComposite(ref: string | undefined): boolean {
    return this.partsOf(ref).length > 0;
  }

  /**
   * The element types a root (a methodology version) is composed of, as its editor lists them: those naming an
   * editor, an abstract base (one a listed type extends) left out, sorted by name.
   */
  sectionsOf(root: string): string[] {
    const parts = this.partsOf(root).filter((t) => this.editor(t));
    const bases = new Set(parts.flatMap((t) => this.type(t)?.ancestors ?? []));
    return parts.filter((t) => !bases.has(t)).sort();
  }

  /** Namespaces of the published and built-in domains (the targets a methodology can name). */
  namespaces(): string[] {
    const ns = new Set(Object.keys(this.domains));
    for (const r of this.types.keys()) ns.add(splitType(r).namespace);
    ns.delete('');
    return [...ns].sort();
  }

}

export const typeCatalog = $state<{ cat: TypeCatalog; loaded: boolean; error: string }>({ cat: new TypeCatalog(), loaded: false, error: '' });

let pending: Promise<TypeCatalog> | undefined;

/** The catalogue in force, loaded on first use; `force` reloads it (after a domain is published). */
export function loadTypes(force = false): Promise<TypeCatalog> {
  if (pending && !force) return pending;
  const p = registry
    .listTypes()
    .then((res) => {
      const cat = new TypeCatalog(res.types ?? [], res.linkTypes ?? [], res.domains ?? {});
      typeCatalog.cat = cat;
      typeCatalog.loaded = true;
      typeCatalog.error = '';
      return cat;
    })
    .catch((e: unknown) => {
      if (pending === p) pending = undefined;
      typeCatalog.error = e instanceof Error ? e.message : String(e);
      return typeCatalog.cat;
    });
  pending = p;
  return p;
}
