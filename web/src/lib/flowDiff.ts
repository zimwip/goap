// The logic of the Compare dialog (ADR 0083): which two scopes are compared by default, and how the impact diff the
// graph returns (`DiffFlows`) is grouped, filtered and worded. No component state here.
import type { FieldChange, Flow, ImpactDiff, JsonValue } from './api';
import { MAIN_SCOPE, scopeName } from './changeScope';

export type Category = 'added' | 'removed' | 'modified';

/** The categories, in the order the dialog lists them. */
export const CATEGORIES: readonly Category[] = ['added', 'removed', 'modified'];

export const CATEGORY_LABEL: Record<Category, string> = { added: 'Added', removed: 'Removed', modified: 'Modified' };

/** A scope to compare: the main flow, or an option (open or decided). */
export interface ScopeChoice {
  id: string;
  label: string;
  /** the option's status (empty for the main flow) */
  status: string;
}

/** The two scopes compared. */
export interface Sides {
  left: string;
  right: string;
}

/** The scopes the selectors offer: the main flow, then every option of the change, open ones first. */
export function scopeChoices(options: Flow[]): ScopeChoice[] {
  const rank = (o: Flow) => (o.status === 'open' ? 0 : 1);
  const opts = options
    .map((o, i) => ({ o, i }))
    .sort((a, b) => rank(a.o) - rank(b.o) || a.i - b.i)
    .map(({ o }) => ({ id: o.id ?? '', label: scopeName(options, o.id ?? ''), status: o.optionStatus ?? o.status ?? '' }));
  return [{ id: MAIN_SCOPE, label: 'Main flow', status: '' }, ...opts];
}

/** The scopes the dialog starts on: the main flow on the left; on the right the option the scope bar looks at, else the
 * first open option, else the first option; the main flow when the change has none. */
export function defaultSides(options: Flow[], scope: string): Sides {
  const looked = options.find((o) => o.id === scope);
  const first = options.find((o) => o.status === 'open') ?? options[0];
  return { left: MAIN_SCOPE, right: looked?.id ?? first?.id ?? MAIN_SCOPE };
}

/** The sides kept when the options change under the dialog: a scope that no longer exists falls back to the defaults. */
export function reconcileSides(sides: Sides, options: Flow[], scope: string): Sides {
  const known = new Set(scopeChoices(options).map((c) => c.id));
  if (known.has(sides.left) && known.has(sides.right)) return sides;
  return defaultSides(options, scope);
}

export const swapSides = (s: Sides): Sides => ({ left: s.right, right: s.left });

/** The same scope on both sides: nothing to compare. */
export const sameScope = (s: Sides): boolean => s.left === s.right;

/** The number of impacts of each category. */
export function countByCategory(impacts: ImpactDiff[]): Record<Category, number> {
  const out: Record<Category, number> = { added: 0, removed: 0, modified: 0 };
  for (const i of impacts) if (i.category && i.category in out) out[i.category as Category]++;
  return out;
}

/** One category of the list: its total count and the impacts that pass the filter. */
export interface Group {
  category: Category;
  label: string;
  total: number;
  items: ImpactDiff[];
}

/** The impacts grouped by category (added, removed, modified); a hidden category has no items, an empty one no group. */
export function groupImpacts(impacts: ImpactDiff[], hidden: ReadonlySet<Category>): Group[] {
  const out: Group[] = [];
  for (const category of CATEGORIES) {
    const all = impacts.filter((i) => i.category === category);
    if (all.length) out.push({ category, label: CATEGORY_LABEL[category], total: all.length, items: hidden.has(category) ? [] : all });
  }
  return out;
}

/** The filter after a chip was clicked: the category is shown or hidden. */
export function toggleCategory(hidden: ReadonlySet<Category>, c: Category): Set<Category> {
  const next = new Set(hidden);
  if (!next.delete(c)) next.add(c);
  return next;
}

/** The line under the groups: how many identical impacts are not displayed. */
export function identicalLine(n: number): string {
  return n === 1 ? '1 identical impact hidden' : `${n} identical impacts hidden`;
}

/** The scope in which an impact of the diff is looked at: the one that holds it (the right one, for a modified impact). */
export function scopeOf(d: ImpactDiff, sides: Sides): string {
  return d.category === 'removed' ? sides.left : sides.right;
}

/** A value as the dialog shows it: strings as they are, anything else as JSON, absent as a dash. */
export function formatValue(v: JsonValue | undefined): string {
  if (v === undefined || v === null) return '—';
  return typeof v === 'string' ? (v === '' ? '""' : v) : JSON.stringify(v);
}

/** A text cut to `max` characters (with an ellipsis) and whether it was. */
export function clip(text: string, max = 80): { text: string; clipped: boolean } {
  return text.length > max ? { text: `${text.slice(0, max)}…`, clipped: true } : { text, clipped: false };
}

/** A change worded for a row: `title: old → new`, `state: …`, `+ link satisfies NEED-1`. */
export interface ChangeLine {
  op: string;
  /** what changed: the property, "state", "owner", or "link <type> <target>" */
  label: string;
  old?: string;
  new?: string;
}

export function describeChange(c: FieldChange): ChangeLine {
  const op = c.op ?? 'changed';
  if (c.kind === 'link') {
    const label = `link ${c.name ?? ''} ${c.target ?? ''}`.trim();
    // the properties of a link, when they changed on the same link
    return op === 'changed' ? { op, label, old: formatValue(c.old), new: formatValue(c.new) } : { op, label };
  }
  const label = c.kind === 'state' || c.kind === 'owner' ? c.kind : (c.name ?? '');
  return { op, label, old: op === 'added' ? undefined : formatValue(c.old), new: op === 'removed' ? undefined : formatValue(c.new) };
}
