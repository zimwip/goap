// What the contextual helper is told (ADR 0086): the active tab, the node or change it is about, the text the user
// selected, and the registered fields of the tab. The server caps what it accepts (modelgw `MaxSuggest*`): the same
// limits are applied here so that a request is never refused for its size.
import type { SuggestContext, SuggestField } from '../api';
import type { Tab } from '../shell/types';
import { valueText } from '../attributes';
import { fieldsOfTab } from '../assist/registry.svelte';

export const MAX_FIELDS = 60;
export const MAX_SELECTION = 2000;
/** the budget of the current values of all the fields (the server accepts 32 KiB of context in all) */
const VALUE_BUDGET = 16 * 1024;
const MAX_VALUE = 1000;

/** The params that name what a tab is about, in order of preference. */
const SUBJECT_PARAMS = ['change', 'changeId', 'key', 'node', 'id', 'name'];

function clip(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n) + '…';
}

/** The node or change a tab is about: opaque, the first of its params that names one. */
export function subjectOf(tab: Pick<Tab, 'params'> | undefined): string {
  if (!tab) return '';
  for (const k of SUBJECT_PARAMS) if (tab.params[k]) return tab.params[k];
  return '';
}

/** The text selected in the page (empty when nothing is, or in a context without a selection). */
export function selectedText(): string {
  const s = typeof window !== 'undefined' && window.getSelection ? window.getSelection()?.toString().trim() : '';
  return clip(s ?? '', MAX_SELECTION);
}

/** The context of a tab: its registered fields (the first `MAX_FIELDS`), with their current value as JSON text. */
export function collectContext(tab: Tab | undefined): SuggestContext {
  const regs = tab ? fieldsOfTab(tab.id).slice(0, MAX_FIELDS) : [];
  const per = Math.max(50, Math.min(MAX_VALUE, Math.floor(VALUE_BUDGET / Math.max(1, regs.length))));
  const fields: SuggestField[] = regs.map(({ spec }) => {
    const cur = spec.get();
    const f: SuggestField = { id: spec.id, label: spec.label };
    if (spec.type) f.type = spec.type;
    if (spec.enum?.length) f.enumValues = spec.enum;
    if (spec.description) f.description = spec.description;
    const text = cur === undefined || cur === null || cur === '' ? '' : typeof cur === 'string' ? JSON.stringify(cur) : valueText(cur);
    if (text) f.currentValue = clip(text, per);
    if (spec.readOnly) f.readOnly = true;
    return f;
  });
  return { tab: { kind: tab?.kind ?? '', params: { ...(tab?.params ?? {}) } }, subject: subjectOf(tab), selection: selectedText(), fields };
}
