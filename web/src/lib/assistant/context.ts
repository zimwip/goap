// What the assistant is told with each message (ADR 0092): three layers, the most specific first on the server —
// the focus (the element the person acts on, the selection, the open dialog, the action in progress, the errors on
// screen, the last action), the screen (kind, title, summary, entities) and the application (project, tab). The
// screen and the focus come from the views of the active tab (`assist/registry.svelte.ts`), so the context ZOOMS on
// what is being done: the focused entity is listed first, the entities the view ranks first follow, and whatever does
// not fit the caps is cut from the end. Never the content of a form: only the selection the person made, and a value
// a view explicitly shares (`FieldSpec.share`). The server caps what it accepts (assistantsvc `Context.normalise`);
// the same limits are applied here so a request is never refused for its size.
import type { AssistantContext, AssistantEntity } from '../api';
import type { Tab } from '../shell/types';
import { capturedFocus, capturedSelection, MAX_SELECTION_BYTES } from '../assist/capture';
import { entityOfElement, focusContext, screenContext, sharedValue } from '../assist/registry.svelte';
import { lastAction } from '../assist/recorder';

export { MAX_SELECTION_BYTES };
export const MAX_ENTITIES = 40;
export const MAX_ERRORS = 10;
export const MAX_PROPS = 6;
/** the server accepts 8 KiB rendered; the JSON of the request is kept a little under */
export const MAX_CONTEXT_BYTES = 7 * 1024;
const MAX_PARAMS = 10;
const MAX = { param: 200, kind: 60, id: 100, label: 200, summary: 600, action: 300, error: 200, prop: 80, key: 40 };

const enc = new TextEncoder();
const size = (v: unknown): number => enc.encode(JSON.stringify(v)).length;

/** `s` cut to at most `n` UTF-8 bytes (never in the middle of a character). */
export function clipBytes(s: string, n: number): string {
  if (enc.encode(s).length <= n) return s;
  let out = '';
  let used = 0;
  for (const ch of s) {
    const b = enc.encode(ch).length;
    if (used + b > n) break;
    out += ch;
    used += b;
  }
  return out;
}

function entityOf(e: AssistantEntity, shown?: string): AssistantEntity {
  const out: AssistantEntity = { type: clipBytes(e.type, MAX.kind), id: clipBytes(e.id, MAX.id) };
  if (e.label) out.label = clipBytes(e.label, MAX.label);
  if (e.state) out.state = clipBytes(e.state, MAX.kind);
  const entries = Object.entries(e.props ?? {}).filter(([, v]) => v !== '' && v !== undefined);
  if (shown !== undefined) entries.push(['value', shown]);
  if (entries.length) out.props = Object.fromEntries(entries.slice(0, MAX_PROPS).map(([k, v]) => [clipBytes(k, MAX.key), clipBytes(String(v), MAX.prop)]));
  return out;
}

/** The context of one turn. Empty fields are left out. */
export function assistantContext(tab: Tab | undefined, project: string, now: number = Date.now()): AssistantContext {
  const ctx: AssistantContext = {};
  const tid = tab?.id ?? '';

  // app: always
  const app: NonNullable<AssistantContext['app']> = {};
  if (tab) {
    const params: Record<string, string> = {};
    for (const [k, v] of Object.entries(tab.params).slice(0, MAX_PARAMS)) params[k] = clipBytes(String(v), MAX.param);
    app.tab = { kind: tab.kind, params };
  }
  if (project) app.project = project;
  if (app.tab || app.project) ctx.app = app;

  // focus: what the person is doing
  const fi = tab ? focusContext(tid) : {};
  const focusEl = capturedFocus(now);
  const element = fi.element ?? (tab ? entityOfElement(focusEl, tid) : undefined);
  const focus: NonNullable<AssistantContext['focus']> = {};
  if (element) focus.element = { type: clipBytes(element.type, MAX.kind), id: clipBytes(element.id, MAX.id), ...(element.label ? { label: clipBytes(element.label, MAX.label) } : {}) };
  const sel = clipBytes(capturedSelection(now), MAX_SELECTION_BYTES);
  if (sel) focus.selection = sel;
  if (fi.dialogKind) focus.dialogKind = clipBytes(fi.dialogKind, MAX.kind);
  if (fi.dialogTitle) focus.dialogTitle = clipBytes(fi.dialogTitle, MAX.label);
  if (fi.pendingAction) focus.pendingAction = clipBytes(fi.pendingAction, MAX.action);
  const errors = (fi.errors ?? []).filter(Boolean).slice(0, MAX_ERRORS).map((e) => clipBytes(e, MAX.error));
  if (errors.length) focus.errors = errors;
  const last = lastAction(now);
  if (last) focus.lastAction = clipBytes(last, MAX.action);
  if (Object.keys(focus).length) ctx.focus = focus;

  // screen: what it shows, the focused entity first
  const s = tab ? screenContext(tid) : {};
  const shown = element?.type === 'field' ? sharedValue(focusEl, tid) : undefined;
  const isFocus = (e: AssistantEntity) => !!element && e.type === element.type && e.id === element.id;
  const seen = new Set<string>();
  let entities = (s.entities ?? [])
    .filter((e) => (e.type || e.id || e.label) && !seen.has(`${e.type}\u0000${e.id}`) && !!seen.add(`${e.type}\u0000${e.id}`))
    .map((e) => entityOf(e, isFocus(e) ? shown : undefined));
  const at = entities.findIndex((e) => !!element && e.type === element.type && e.id === element.id);
  if (at > 0) entities = [entities[at], ...entities.slice(0, at), ...entities.slice(at + 1)];
  entities = entities.slice(0, MAX_ENTITIES);
  const screen: NonNullable<AssistantContext['screen']> = {};
  const kind = s.kind || tab?.kind;
  if (kind) screen.kind = clipBytes(kind, MAX.kind);
  if (s.title) screen.title = clipBytes(s.title, MAX.label);
  if (s.summary) screen.summary = clipBytes(s.summary, MAX.summary);
  if (entities.length) screen.entities = entities;
  if (Object.keys(screen).length) ctx.screen = screen;

  // the byte budget: entities go from the end, the focused one last
  while (ctx.screen?.entities?.length && size(ctx) > MAX_CONTEXT_BYTES) ctx.screen.entities.pop();
  if (ctx.screen && !ctx.screen.entities?.length) delete ctx.screen.entities;
  return ctx;
}
