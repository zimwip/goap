// The fields the contextual helper may fill (ADR 0086). A form registers each of its fields with the
// `use:assistField` action: the element (the bubble points at it), what the helper needs to know about it, and a
// setter that goes through the form's own edit path. Fields live in memory for as long as the element does.
import { activeTab } from '../shell/tabs.svelte';

export interface FieldSpec {
  /** unique within the tab (the attribute name for a node form) */
  id: string;
  label: string;
  /** string | number | boolean | date | enum | json; empty: any */
  type?: string;
  /** the allowed values of an enum */
  enum?: string[];
  description?: string;
  /** the current value */
  get: () => unknown;
  /** sets a proposed value, through the normal edit path of the form (the user still saves) */
  set: (value: unknown) => void;
  readOnly?: boolean;
}

export interface RegisteredField {
  spec: FieldSpec;
  el: HTMLElement;
  /** the tab the field was shown in */
  tab: string;
}

const DOCUMENT_POSITION_FOLLOWING = 4; // Node.DOCUMENT_POSITION_FOLLOWING

const fields = new Map<symbol, RegisteredField>();

/** Bumped on each change, so that a reader inside an effect re-runs. */
const stamp = $state({ n: 0 });

/** Registers a field; returns the handle to update or remove it. */
export function registerField(el: HTMLElement, spec: FieldSpec, tab: string = activeTab()?.id ?? ''): { update: (spec: FieldSpec) => void; destroy: () => void } {
  const key = Symbol(spec.id);
  fields.set(key, { spec, el, tab });
  stamp.n++;
  return {
    update(next) {
      const cur = fields.get(key);
      if (cur) cur.spec = next;
      stamp.n++;
    },
    destroy() {
      if (fields.delete(key)) stamp.n++;
    },
  };
}

/** Svelte action: `<input use:assistField={{ id, label, type, get, set }} />`. */
export function assistField(el: HTMLElement, spec: FieldSpec) {
  const h = registerField(el, spec);
  return { update: h.update, destroy: h.destroy };
}

/** The fields shown in a tab (the active tab by default), in the order of the page. */
export function fieldsOfTab(tab: string = activeTab()?.id ?? ''): RegisteredField[] {
  void stamp.n;
  const out = [...fields.values()].filter((f) => f.tab === tab && f.el.isConnected);
  if (out.every((f) => typeof f.el.compareDocumentPosition === 'function')) {
    out.sort((a, b) => (a.el.compareDocumentPosition(b.el) & DOCUMENT_POSITION_FOLLOWING ? -1 : 1));
  }
  return out;
}

/** The registered field of an id in the tab. */
export function fieldOf(id: string, tab: string = activeTab()?.id ?? ''): RegisteredField | undefined {
  return fieldsOfTab(tab).find((f) => f.spec.id === id);
}

/** Does the tab show at least one field the helper may fill? */
export function hasFields(tab: string = activeTab()?.id ?? ''): boolean {
  return fieldsOfTab(tab).some((f) => !f.spec.readOnly);
}
