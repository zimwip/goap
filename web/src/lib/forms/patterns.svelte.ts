// The patterns of the form designer (ADR 0115 §9): the ones shipped with the web (PATTERNS) and the ones the platform
// shares as graph data, `platform@FormPattern` nodes (FP:<name>) at the head of main of the platform namespace, saved
// and retired through a change applied on main.
import { errorMessage, type GraphNode } from '../api';
import { applyOnMain, createNodeItem, findNode, headGraph, retireNodeItem, updateNodeItem } from '../graphEdit';
import { ns } from '../stores/session.svelte';
import { PATTERNS, type Pattern } from './design';
import type { FormDef } from './types';

export interface SharedPattern extends Pattern {
  /** the node holding it */
  key: string;
}

export const patternType = (): string => `${ns.platform}@FormPattern`;
export const patternKey = (name: string): string => `FP:${name}`;

export const formPatterns = $state({ shared: [] as SharedPattern[], loaded: false, error: '' });

/** A pattern of a FormPattern node (undefined: no usable form). */
export function patternOfNode(n: GraphNode): SharedPattern | undefined {
  const p = (n.props ?? {}) as Record<string, unknown>;
  const form = p['form'];
  if (!form || typeof form !== 'object' || Array.isArray(form)) return undefined;
  const name = typeof p['name'] === 'string' && p['name'] ? p['name'] : (n.key ?? '').replace(/^FP:/, '');
  return {
    key: n.key ?? '',
    name,
    label: typeof p['label'] === 'string' && p['label'] ? p['label'] : name,
    description: typeof p['description'] === 'string' ? p['description'] : '',
    form: form as FormDef,
  };
}

/** The patterns the designer offers: the shipped ones, then the shared ones (a shared one shadows a shipped one of its name). */
export function allPatterns(shared: SharedPattern[] = formPatterns.shared): Pattern[] {
  const names = new Set(shared.map((p) => p.name));
  return [...PATTERNS.filter((p) => !names.has(p.name)), ...shared];
}

export async function loadPatterns(): Promise<void> {
  try {
    const h = await headGraph(ns.platform);
    formPatterns.shared = h.nodes
      .filter((n) => n.type === patternType())
      .flatMap((n) => patternOfNode(n) ?? [])
      .sort((a, b) => a.label.localeCompare(b.label));
    formPatterns.error = '';
  } catch (e) {
    // no commit yet: no shared pattern
    formPatterns.shared = [];
    formPatterns.error = /no commit/.test(errorMessage(e)) ? '' : errorMessage(e);
  } finally {
    formPatterns.loaded = true;
  }
}

/** Saves a form as a shared pattern (created, or updated when the name exists, a retired one restored). */
export async function savePattern(p: { name: string; label: string; description: string; form: FormDef }): Promise<void> {
  const name = p.name.trim();
  if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(name)) throw new Error('A pattern name is letters, digits, _ . or -.');
  if (!p.form.fields?.length) throw new Error('A pattern holds at least one field.');
  const h = await headGraph(ns.platform);
  const key = patternKey(name);
  const props = { name, label: p.label.trim() || name, description: p.description.trim(), form: p.form as never };
  const n = findNode(h, ns.platform, patternType(), key);
  const edit = n ? updateNodeItem(n, props) : createNodeItem(key, patternType(), props);
  await applyOnMain(ns.platform, `Form pattern ${name}`, `Share the form pattern ${name} with the form designer`, h.commitId, [edit]);
  await loadPatterns();
}

/** Retires a shared pattern (never deleted, ADR 0076). */
export async function retirePattern(name: string): Promise<void> {
  const h = await headGraph(ns.platform);
  const n = findNode(h, ns.platform, patternType(), patternKey(name));
  if (!n) throw new Error(`No shared pattern ${name}.`);
  await applyOnMain(ns.platform, `Retire form pattern ${name}`, `The form pattern ${name} is no longer offered`, h.commitId, [retireNodeItem(n)]);
  await loadPatterns();
}
