// Node editors: the editors that node types name in their `editor` property (the built-in domains methodology,
// organisation and platform). Each one maps a graph node
// onto the tab of the editor; a node it cannot show (an element removed from
// its definition, an unknown version) opens in the default node editor.
import { registerNodeEditor } from '../shell/registry';
import type { NodeHandle } from '../shell/types';
import { getDraft } from '../stores/drafts.svelte';
import { KIND_SECTION, itemSpec, methodologySpec } from './editors/methodologyTabs';

const str = (v: unknown): string => (typeof v === 'string' ? v : '');

/**
 * The methodology version a definition node belongs to (ADR 0023): the header is keyed "MV:<name>@<version>", its
 * elements "<header key>/<kind>/<name>". Domains are not graph data: they open from the Domains explorer.
 */
function methodologyOf(n: NodeHandle): { name: string; version: string } | undefined {
  const m = /^MV:([^@/]+)@([^/]+)/.exec(n.key);
  return m ? { name: m[1], version: m[2] } : undefined;
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
