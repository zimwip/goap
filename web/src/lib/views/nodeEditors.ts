// Node editors: the editors that node types name in their `editor` property (the built-in meta-domains
// methodology and domain, the built-in organisation and platform domains). Each one maps a graph node
// onto the tab of the editor; a node it cannot show (an element removed from
// its definition, an unknown version) opens in the default node editor.
import { registerNodeEditor } from '../shell/registry';
import type { NodeHandle } from '../shell/types';
import { getDraft } from '../stores/drafts.svelte';
import { getDomainDraft } from '../stores/domains.svelte';
import { KIND_SECTION, itemSpec, methodologySpec } from './editors/methodologyTabs';
import { algorithmSpec, domainSpec, instanceSpec } from './editors/domainTabs';

const str = (v: unknown): string => (typeof v === 'string' ? v : '');

/**
 * The methodology or domain version a stored definition node belongs to (ADR 0023): the header is keyed
 * "MV:<name>@<version>" / "DV:<name>@<version>", its elements "<header key>/<kind>/<name>".
 */
function ownerOf(key: string): { kind: 'methodology' | 'domain'; name: string; version: string } | undefined {
  const m = /^(MV|DV):([^@/]+)@([^/]+)/.exec(key);
  if (!m) return undefined;
  return { kind: m[1] === 'MV' ? 'methodology' : 'domain', name: m[2], version: m[3] };
}

/** The methodology version a definition node belongs to (MV:<name>@<version>...). */
function methodologyOf(n: NodeHandle): { name: string; version: string } | undefined {
  const o = ownerOf(n.key);
  return o?.kind === 'methodology' ? o : undefined;
}

/** A live element of a definition (a removed one stays as a node with removed=true). */
const element = (n: NodeHandle) => (n.props.removed === true ? '' : str(n.props.name));

registerNodeEditor({
  name: 'methodology',
  title: 'Methodology editor',
  open: (n) => {
    const o = methodologyOf(n);
    return o ? methodologySpec(o.name, o.version) : undefined;
  },
});

registerNodeEditor({
  name: 'domain',
  title: 'Domain editor',
  open: (n) => {
    const o = ownerOf(n.key);
    return o?.kind === 'domain' ? domainSpec(o.name, o.version) : undefined;
  },
});

// agents, actions, conditions and goals of a methodology version: their own tab, on the
// methodology draft
for (const [kind, title] of [
  ['agent', 'Agent editor'],
  ['action', 'Action editor'],
  ['condition', 'Condition editor'],
  ['goal', 'Goal editor'],
] as const) {
  registerNodeEditor({
    name: kind,
    title,
    open: async (n) => {
      const name = element(n);
      const o = name ? methodologyOf(n) : undefined;
      if (!o) return undefined;
      const d = getDraft(o.name, o.version);
      await d.ensureLoaded();
      const section = KIND_SECTION[kind];
      const i = d.indexOf(section, '', name);
      return i >= 0 ? itemSpec(d, section, d.items(section)[i]) : undefined;
    },
  });
}

// algorithms and their instances of a domain version
registerNodeEditor({
  name: 'algorithm',
  title: 'Algorithm editor',
  open: async (n) => {
    const o = ownerOf(n.key);
    const name = element(n);
    if (o?.kind !== 'domain' || !name) return undefined;
    const d = getDomainDraft(o.name, o.version);
    await d.ensureLoaded();
    const a = d.form.algorithms.find((x) => x.name === name);
    return a ? algorithmSpec(o.name, o.version, a.uid, a.name) : undefined;
  },
});

registerNodeEditor({
  name: 'instance',
  title: 'Algorithm instance editor',
  open: async (n) => {
    const o = ownerOf(n.key);
    const name = element(n);
    if (o?.kind !== 'domain' || !name) return undefined;
    const d = getDomainDraft(o.name, o.version);
    await d.ensureLoaded();
    const i = d.form.instances.find((x) => x.name === name);
    return i ? instanceSpec(o.name, o.version, i.uid, i.name) : undefined;
  },
});

// node types, link types and lifecycles: the methodology or domain editor, on the element
const DEF_FIELDS: Record<string, string> = { 'domain@NodeType': 'nodeTypes', 'domain@LinkType': 'linkTypes', 'domain@Lifecycle': 'lifecycles' };

registerNodeEditor({
  name: 'definition',
  title: 'Definition editor',
  open: (n) => {
    const o = ownerOf(n.key);
    if (!o) return undefined;
    const spec = o.kind === 'methodology' ? methodologySpec(o.name, o.version) : domainSpec(o.name, o.version);
    const field = DEF_FIELDS[n.type];
    const pos = n.props.position;
    return field && typeof pos === 'number' && !n.props.removed ? { ...spec, reveal: `${field}[${pos}]` } : spec;
  },
});

// organisation: a unit's page shows the unit and its adapters (keyed "ADP:<unit>/<mcp>")
registerNodeEditor({
  name: 'unit',
  title: 'Organisation editor',
  open: (n) => {
    if (n.key.startsWith('ADP:')) {
      const unit = n.key.slice(4, n.key.lastIndexOf('/'));
      return unit ? { kind: 'unit', params: { key: unit } } : undefined;
    }
    return n.key ? { kind: 'unit', params: { key: n.key } } : undefined;
  },
});

// policies and users
registerNodeEditor({
  name: 'access',
  title: 'Access editor',
  open: () => ({ kind: 'policies', params: {} }),
});

registerNodeEditor({
  name: 'mcp',
  title: 'MCP editor',
  open: (n) => (str(n.props.name) ? { kind: 'mcp', params: { name: str(n.props.name) } } : undefined),
});

registerNodeEditor({
  name: 'adapter',
  title: 'Adapter editor',
  open: (n) => (str(n.props.name) ? { kind: 'adapter', params: { name: str(n.props.name) } } : undefined),
});
