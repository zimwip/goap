// What the assistant is told with each message (ADR 0087): the active tab, the text the user selected and the active
// project (the layered collector of ADR 0092 replaces this). Never the content of a form: only the selection, which the user chose to
// select. The server caps what it accepts (assistantsvc `MaxSelectionBytes`, `MaxContextBytes`); the same limits are
// applied here so a request is never refused for its size.
import type { AssistantContext } from '../api';
import type { Tab } from '../shell/types';
import { selectedText } from '../helper/context';

export const MAX_SELECTION_BYTES = 2000;
const MAX_PARAMS = 10;
const MAX_PARAM_BYTES = 200;

const enc = new TextEncoder();

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

/** The context of one turn. Empty fields are left out. (The layered collector, ADR 0092, replaces this one.) */
export function assistantContext(tab: Tab | undefined, project: string, selection: string = selectedText()): AssistantContext {
  const ctx: AssistantContext = {};
  const app: NonNullable<AssistantContext['app']> = {};
  if (tab) {
    const params: Record<string, string> = {};
    for (const [k, v] of Object.entries(tab.params).slice(0, MAX_PARAMS)) params[k] = clipBytes(String(v), MAX_PARAM_BYTES);
    app.tab = { kind: tab.kind, params };
  }
  if (project) app.project = project;
  if (app.tab || app.project) ctx.app = app;
  const sel = clipBytes(selection, MAX_SELECTION_BYTES);
  if (sel) ctx.focus = { selection: sel };
  return ctx;
}
