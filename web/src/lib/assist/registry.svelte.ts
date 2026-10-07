// The registry a screen uses to be operated by the assistant and the helper (ADR 0092, which unifies ADR 0086's field
// registry with the screen tools). For as long as a view is mounted it registers
//   - a context provider per layer: `screen()` (what the view shows: kind, title, summary, entities) and `focus()`
//     (what the person is doing: the element, the open dialog, the action in progress, the errors on screen);
//   - screen tools: implementations of the catalog's tools (`catalog.ts`), run through the screen's own edit and
//     navigation path, which can be enabled or disabled as the state changes (a disabled tool is not offered);
//   - fields (`use:assistField`): the inputs the helper and the derived `set_field` tool may fill, with the setter of
//     the form; and targets (`use:assistTarget`): elements a proposal can point at.
// Everything is in memory, scoped to the tab it was registered in, and removed when the view is destroyed. A tool is
// only ever run while it is registered: what the web reads from a stored message may be older than the screen.
import { activeTab } from '../shell/tabs.svelte';
import type { AssistantEntity, UiTool } from '../api/types/assistant';
import { descriptor, fieldGuidance, isToolName, type ToolName } from './catalog';
import { MAX_ENUM, MAX_TOOLS, validateArgs, validateDescriptor } from './schema';

export interface ScreenInfo {
  kind?: string;
  title?: string;
  summary?: string;
  entities?: AssistantEntity[];
}

export interface FocusInfo {
  element?: { type: string; id: string; label?: string };
  dialogKind?: string;
  dialogTitle?: string;
  /** what the person is in the middle of */
  pendingAction?: string;
  errors?: string[];
}

export type ToolArgs = Record<string, unknown>;

export interface ToolImpl {
  /** a name of the catalog */
  name: string;
  /** runs the tool through the screen's own path; returns (or throws) an error message when it cannot */
  run: (args: ToolArgs) => Promise<string | void> | string | void;
  /** false: not applicable now, so not offered (read when the descriptors are built) */
  enabled?: () => boolean;
  /** the target key (`type:id`) the call acts on, to point at it */
  targetOf?: (args: ToolArgs) => string | undefined;
  /** adapts the catalog descriptor to the current state (an enum of the ids on screen) */
  describe?: (base: UiTool) => UiTool;
}

export interface AssistRegistration {
  /** the tab the view is shown in (the active one by default) */
  tab?: string;
  screen?: () => ScreenInfo | undefined;
  focus?: () => FocusInfo | undefined;
  tools?: ToolImpl[];
}

export interface FieldSpec {
  /** unique within the tab (the attribute name for a node form) */
  id: string;
  label: string;
  /** string | number | boolean | date | enum | json; empty: any */
  type?: string;
  /** the allowed values of an enum */
  enum?: string[];
  /** the tooltip of the attribute */
  description?: string;
  /** what the model needs to feed it (the catalog's by id otherwise) */
  guidance?: string;
  required?: boolean;
  /** the current value */
  get: () => unknown;
  /** sets a proposed value, through the normal edit path of the form (the user still saves) */
  set: (value: unknown) => void;
  readOnly?: boolean;
  /** a dedicated tool fills it: `set_field` leaves it out */
  tool?: string;
  /** the value may be shown to the assistant when the field has the focus (default: never) */
  share?: boolean;
}

export interface RegisteredField {
  spec: FieldSpec;
  el: HTMLElement;
  /** the tab the field was shown in */
  tab: string;
}

interface Reg extends AssistRegistration {
  tab: string;
}
interface Target {
  key: string;
  el: HTMLElement;
  tab: string;
}

const DOCUMENT_POSITION_FOLLOWING = 4; // Node.DOCUMENT_POSITION_FOLLOWING

const regs = new Map<symbol, Reg>();
const fields = new Map<symbol, RegisteredField>();
const targets = new Map<symbol, Target>();

/** Bumped on each change, so that a reader inside an effect re-runs. */
const stamp = $state({ n: 0 });
const bump = (): void => {
  stamp.n++;
};

const currentTab = (): string => activeTab()?.id ?? '';

export type ToolResult = { ok: true } | { ok: false; error: string };

// --- fields (the helper, ADR 0086) -------------------------------------------------------------------------------------

/** Registers a field; returns the handle to update or remove it. */
export function registerField(el: HTMLElement, spec: FieldSpec, tab: string = currentTab()): { update: (spec: FieldSpec) => void; destroy: () => void } {
  const key = Symbol(spec.id);
  fields.set(key, { spec, el, tab });
  bump();
  return {
    update(next) {
      const cur = fields.get(key);
      if (cur) cur.spec = next;
      bump();
    },
    destroy() {
      if (fields.delete(key)) bump();
    },
  };
}

/** Svelte action: `<input use:assistField={{ id, label, type, get, set }} />`. */
export function assistField(el: HTMLElement, spec: FieldSpec) {
  const h = registerField(el, spec);
  return { update: h.update, destroy: h.destroy };
}

/** The fields shown in a tab (the active tab by default), in the order of the page. */
export function fieldsOfTab(tab: string = currentTab()): RegisteredField[] {
  void stamp.n;
  const out = [...fields.values()].filter((f) => f.tab === tab && f.el.isConnected);
  if (out.every((f) => typeof f.el.compareDocumentPosition === 'function')) {
    out.sort((a, b) => (a.el.compareDocumentPosition(b.el) & DOCUMENT_POSITION_FOLLOWING ? -1 : 1));
  }
  return out;
}

/** The registered field of an id in the tab. */
export function fieldOf(id: string, tab: string = currentTab()): RegisteredField | undefined {
  return fieldsOfTab(tab).find((f) => f.spec.id === id);
}

/** Does the tab show at least one field the helper may fill? */
export function hasFields(tab: string = currentTab()): boolean {
  return fieldsOfTab(tab).some((f) => !f.spec.readOnly);
}

/** A value given as text, in the type of the field (the form's setter then takes it as a typed value). */
export function coerceFieldValue(spec: FieldSpec, text: string): { ok: true; value: unknown } | { ok: false; error: string } {
  switch (spec.type) {
    case 'number': {
      const n = Number(text);
      return text.trim() !== '' && Number.isFinite(n) ? { ok: true, value: n } : { ok: false, error: `“${spec.label}” takes a number, not “${text}”.` };
    }
    case 'boolean':
      return text === 'true' || text === 'false' ? { ok: true, value: text === 'true' } : { ok: false, error: `“${spec.label}” takes true or false.` };
    case 'enum':
      return !spec.enum || spec.enum.includes(text) ? { ok: true, value: text } : { ok: false, error: `“${spec.label}” takes one of ${spec.enum.join(', ')}.` };
    case 'json':
      try {
        return { ok: true, value: JSON.parse(text) };
      } catch {
        return { ok: false, error: `“${spec.label}” takes JSON.` };
      }
    default:
      return { ok: true, value: text };
  }
}

// --- targets -----------------------------------------------------------------------------------------------------------

/** Registers an element a proposal can point at, under a `type:id` key. */
export function registerTarget(el: HTMLElement, key: string, tab: string = currentTab()): { update: (key: string) => void; destroy: () => void } {
  const id = Symbol(key);
  const t: Target = { key, el, tab };
  targets.set(id, t);
  bump();
  return {
    update(next) {
      t.key = next;
      bump();
    },
    destroy() {
      if (targets.delete(id)) bump();
    },
  };
}

/** Svelte action: `<tr use:assistTarget={`impact:${id}`}>`. */
export function assistTarget(el: HTMLElement, key: string) {
  const h = registerTarget(el, key);
  return { update: h.update, destroy: h.destroy };
}

/** The element of a target key in the tab; `field:<id>` is a registered field. */
export function targetElement(key: string, tab: string = currentTab()): HTMLElement | undefined {
  void stamp.n;
  for (const t of targets.values()) if (t.tab === tab && t.key === key && t.el.isConnected) return t.el;
  if (key.startsWith('field:')) return fieldOf(key.slice(6), tab)?.el;
  return undefined;
}

export const HIGHLIGHT_CLASS = 'assist-target';

/** Scrolls the target into view and outlines it; the returned function removes the outline. */
export function highlightTarget(key: string | undefined, tab: string = currentTab()): () => void {
  const el = key ? targetElement(key, tab) : undefined;
  if (!el) return () => {};
  el.scrollIntoView?.({ block: 'nearest', behavior: 'smooth' });
  el.classList?.add(HIGHLIGHT_CLASS);
  return () => el.classList?.remove(HIGHLIGHT_CLASS);
}

/** Scrolls the target into view, nothing else. */
export function revealTarget(key: string, tab: string = currentTab()): boolean {
  const el = targetElement(key, tab);
  el?.scrollIntoView?.({ block: 'nearest', behavior: 'smooth' });
  return !!el;
}

/** What a focused element is, among the fields and targets of the tab. */
export function entityOfElement(el: unknown, tab: string = currentTab()): { type: string; id: string; label?: string } | undefined {
  const node = el as Node | null | undefined;
  if (!node) return undefined;
  const has = (e: HTMLElement) => e === node || !!e.contains?.(node);
  const f = fieldsOfTab(tab).find((x) => has(x.el));
  if (f) return { type: 'field', id: f.spec.id, label: f.spec.label };
  for (const t of targets.values()) {
    if (t.tab !== tab || !t.el.isConnected || !has(t.el)) continue;
    const i = t.key.indexOf(':');
    if (i > 0) return { type: t.key.slice(0, i), id: t.key.slice(i + 1) };
  }
  return undefined;
}

// --- screens and tools -------------------------------------------------------------------------------------------------

/** Registers what a view offers the assistant, for as long as it is mounted; returns the cleanup. */
export function registerAssist(reg: AssistRegistration): () => void {
  for (const t of reg.tools ?? []) if (!isToolName(t.name)) throw new Error(`assist: “${t.name}” is not a tool of the catalog`);
  const key = Symbol('assist');
  regs.set(key, { ...reg, tab: reg.tab ?? currentTab() });
  bump();
  return () => {
    if (regs.delete(key)) bump();
  };
}

/** Svelte action: a tool bound to an element (it is the target of its calls). */
export function assistTool(el: HTMLElement, impl: ToolImpl) {
  let off = () => {};
  let tgt = () => {};
  const put = (i: ToolImpl) => {
    off();
    tgt();
    const key = `tool:${i.name}`;
    tgt = registerTarget(el, key).destroy;
    off = registerAssist({ tools: [{ targetOf: () => key, ...i }] });
  };
  put(impl);
  return {
    update: put,
    destroy() {
      off();
      tgt();
    },
  };
}

const safe = <T>(fn: (() => T) | undefined): T | undefined => {
  try {
    return fn?.();
  } catch {
    return undefined;
  }
};

/** `set_field`, derived from the fields of the tab the form registered (those no dedicated tool fills). */
function fieldTool(tab: string): ToolImpl | undefined {
  const fillable = () => fieldsOfTab(tab).filter((f) => !f.spec.readOnly && !f.spec.tool);
  if (!fillable().length) return undefined;
  return {
    name: 'set_field',
    describe: (base) => {
      const ids = fillable().map((f) => f.spec.id);
      const field = ids.length <= MAX_ENUM ? { type: 'enum' as const, enum: ids, description: 'the field id' } : { type: 'string' as const, description: 'the field id' };
      return { ...base, args: { ...base.args, properties: { ...base.args?.properties, field } } };
    },
    targetOf: (a) => (typeof a.field === 'string' ? `field:${a.field}` : undefined),
    run: (a) => {
      const f = fillable().find((x) => x.spec.id === a.field);
      if (!f) return `The field “${String(a.field)}” is no longer on the screen.`;
      const v = coerceFieldValue(f.spec, String(a.value ?? ''));
      if (!v.ok) return v.error;
      f.spec.set(v.value);
    },
  };
}

function toolsOf(tab: string): ToolImpl[] {
  const byName = new Map<string, ToolImpl>();
  for (const r of regs.values()) if (r.tab === tab) for (const t of r.tools ?? []) byName.set(t.name, t);
  const f = fieldTool(tab);
  if (f && !byName.has(f.name)) byName.set(f.name, f);
  return [...byName.values()];
}

function describeOne(impl: ToolImpl): UiTool | undefined {
  if (impl.enabled && safe(impl.enabled) === false) return undefined;
  const base = descriptor(impl.name as ToolName);
  const d = safe(() => (impl.describe ? impl.describe(base) : base)) ?? base;
  const bad = validateDescriptor(d);
  if (bad) {
    console.warn(`assist: tool dropped, ${bad}`);
    return undefined;
  }
  return d;
}

/** The tools the screen offers now, as `Send` takes them (at most 30). */
export function describeTools(tab: string = currentTab()): UiTool[] {
  void stamp.n;
  return toolsOf(tab)
    .flatMap((t) => describeOne(t) ?? [])
    .slice(0, MAX_TOOLS);
}

/** Is the tool registered, and applicable, on the screen now. */
export function hasTool(name: string, tab: string = currentTab()): boolean {
  void stamp.n;
  const t = toolsOf(tab).find((x) => x.name === name);
  return !!t && !!describeOne(t);
}

/** The target key a call of a tool points at (for the proposal card). */
export function targetOfCall(name: string, args: ToolArgs, tab: string = currentTab()): string | undefined {
  const t = toolsOf(tab).find((x) => x.name === name);
  return t ? safe(() => t.targetOf?.(args)) : undefined;
}

/** Runs a registered tool with arguments checked against its current descriptor; never throws. */
export async function runTool(name: string, args: ToolArgs | undefined, tab: string = currentTab()): Promise<ToolResult> {
  const impl = toolsOf(tab).find((x) => x.name === name);
  if (!impl) return { ok: false, error: `The screen no longer offers “${name}”: open the screen it was proposed for and ask again.` };
  const d = describeOne(impl);
  if (!d) return { ok: false, error: `“${name}” is not applicable on the screen any more.` };
  const bad = validateArgs(d.args, args ?? {});
  if (bad) return { ok: false, error: `“${name}” cannot run: ${bad}.` };
  try {
    const r = await impl.run(args ?? {});
    return typeof r === 'string' && r ? { ok: false, error: r } : { ok: true };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) };
  }
}

const regsOf = (tab: string): Reg[] => [...regs.values()].filter((r) => r.tab === tab);

/** The screen layer of a tab: what its views say they show, plus its fields as entities (never their values). */
export function screenContext(tab: string = currentTab()): ScreenInfo {
  void stamp.n;
  const out: ScreenInfo = {};
  const entities: AssistantEntity[] = [];
  const summary: string[] = [];
  for (const r of regsOf(tab)) {
    const s = safe(r.screen);
    if (!s) continue;
    out.kind ||= s.kind;
    out.title ||= s.title;
    if (s.summary) summary.push(s.summary);
    entities.push(...(s.entities ?? []));
  }
  for (const { spec } of fieldsOfTab(tab)) entities.push(fieldEntity(spec));
  if (summary.length) out.summary = summary.join(' ');
  if (entities.length) out.entities = entities;
  return out;
}

function fieldEntity(spec: FieldSpec): AssistantEntity {
  const props: Record<string, string> = {};
  if (spec.type) props.type = spec.type;
  if (spec.enum?.length) props.values = spec.enum.join('|');
  if (spec.required) props.required = 'yes';
  props.guidance = fieldGuidance(spec);
  return { type: 'field', id: spec.id, label: spec.label, ...(spec.readOnly ? { state: 'read-only' } : {}), props };
}

/** The focus layer the views of a tab declare: the first element and dialog, the last pending action, every error. */
export function focusContext(tab: string = currentTab()): FocusInfo {
  void stamp.n;
  const out: FocusInfo = {};
  const errors: string[] = [];
  for (const r of regsOf(tab)) {
    const f = safe(r.focus);
    if (!f) continue;
    out.element ||= f.element;
    out.dialogKind ||= f.dialogKind;
    out.dialogTitle ||= f.dialogTitle;
    if (f.pendingAction) out.pendingAction = f.pendingAction;
    errors.push(...(f.errors ?? []));
  }
  if (errors.length) out.errors = errors;
  return out;
}

/** The registered fields of the tab whose value the assistant may see when the field has the focus. */
export function sharedValue(el: unknown, tab: string = currentTab()): string | undefined {
  const node = el as Node | null | undefined;
  const f = fieldsOfTab(tab).find((x) => x.el === node || x.el.contains?.(node ?? null));
  if (!f?.spec.share) return undefined;
  const v = safe(f.spec.get);
  return v === undefined || v === null ? undefined : typeof v === 'string' ? v : JSON.stringify(v);
}

/** For tests: forgets everything. */
export function resetRegistry(): void {
  regs.clear();
  fields.clear();
  targets.clear();
  bump();
}
