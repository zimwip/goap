// The type catalogue in force (ADR 0012): the resolved node and link types of the published domains and of the
// built-in domains, served by the registry. A type is referenced as "<namespace>@<name>". Loaded once, reloaded
// after a domain is published from the IDE.
import { registry, type LinkTypeInfo, type Lifecycle, type TypeInfo } from '../api';

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

  /** Properties declared by a type, its ancestors' first. */
  properties(ref: string | undefined): string[] {
    return this.type(ref)?.properties ?? [];
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
