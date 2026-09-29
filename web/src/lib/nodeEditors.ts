// Opening a node: a node type may name the editor of its nodes (NodeType
// `editor`, inherited through extends, ADR 0027); every place that opens a node
// goes through openNode, which resolves that editor from the type catalogue and
// falls back to the default node editor (NodeTab) when the type names none, the
// editor is unknown, or it cannot show the node.
import { graph, type GraphNode } from './api';
import { nodeEditor } from './shell/registry';
import { loadTypes } from './stores/types.svelte';
import { openTab } from './shell/tabs.svelte';
import { requestReveal } from './shell/workbench.svelte';
import type { NodeEditor, NodeEditorTarget, NodeHandle, Tab, TabSpec } from './shell/types';

/** The editor named by a node type in the type catalogue (undefined: the default node editor). */
export async function editorOfType(type: string): Promise<NodeEditor | undefined> {
  return nodeEditor((await loadTypes()).editor(type));
}

/** What is known of a node where it is opened: at least its id. */
export type NodeLike = Pick<GraphNode, 'id' | 'key' | 'type' | 'namespace' | 'props'>;

/** Completes a node with its latest version when its type or properties are not known. */
export async function handleOf(n: NodeLike): Promise<NodeHandle> {
  let node = n;
  if ((!n.type || !n.props) && n.id) {
    try {
      node = (await graph.listNodeVersions(n.id)).versions?.reduce<GraphNode | undefined>((a, v) => ((v.version ?? 0) >= (a?.version ?? 0) ? v : a), undefined) ?? n;
    } catch {
      // the default node editor shows the error
    }
  }
  return {
    id: n.id ?? node.id ?? '',
    key: node.key || n.key || '',
    type: node.type ?? '',
    namespace: node.namespace ?? '',
    props: (node.props ?? {}) as Record<string, unknown>,
  };
}

/** Where the editor of its type shows a node (undefined: the default node editor). */
export async function editorTarget(node: NodeHandle): Promise<{ editor: NodeEditor; target: NodeEditorTarget } | undefined> {
  const editor = await editorOfType(node.type);
  if (!editor) return undefined;
  try {
    const target = await editor.open(node);
    return target ? { editor, target } : undefined;
  } catch {
    return undefined;
  }
}

export interface OpenNodeOptions {
  pin?: boolean;
  background?: boolean;
  /** the default node editor, whatever editor the type names (history, relations...) */
  generic?: boolean;
  /** pane of the default node editor */
  pane?: string;
  /** working change of the default node editor */
  change?: string;
  /** flow or option of the working change the node is seen and edited on ('main' or an option id, ADR 0032 §6) */
  flow?: string;
}

/** The tab of a node in the default node editor. */
export function nodeTabSpec(n: NodeLike, opts: Pick<OpenNodeOptions, 'pane' | 'change' | 'flow'> = {}): TabSpec {
  const params: Record<string, string> = { id: n.id ?? '', key: n.key ?? '' };
  if (opts.pane) params.pane = opts.pane;
  if (opts.change) params.change = opts.change;
  if (opts.change && opts.flow) params.flow = opts.flow;
  return { kind: 'node', params };
}

/** Opens the tab of a node editor and reveals its field. */
export function openTarget(target: NodeEditorTarget, opts: OpenNodeOptions): Tab {
  const { reveal, ...spec } = target;
  const tab = openTab(spec, { pin: opts.pin, background: opts.background });
  if (reveal) requestReveal(tab.id, reveal);
  return tab;
}

/** Opens a node in the editor its type names, else in the default node editor. */
export async function openNode(n: NodeLike, opts: OpenNodeOptions = {}): Promise<Tab> {
  if (!opts.generic) {
    const found = await editorTarget(await handleOf(n));
    if (found) return openTarget(found.target, opts);
  }
  return openTab(nodeTabSpec(n, opts), { pin: opts.pin, background: opts.background });
}
