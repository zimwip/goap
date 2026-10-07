// What the assistant is told with each message (ADR 0087): the active tab, the node or change it is about, the text the
// user selected and the active project. Never the content of a form: only the selection, which the user chose to
// select. The server caps what it accepts (assistantsvc `MaxSelectionBytes`, `MaxContextBytes`); the same limits are
// applied here so a request is never refused for its size.
import type { AssistantContext } from '../api';
import type { Tab } from '../shell/types';
import { selectedText, subjectOf } from '../helper/context';

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

/** The context of one turn. Empty fields are left out. */
export function assistantContext(tab: Tab | undefined, project: string, selection: string = selectedText()): AssistantContext {
  const ctx: AssistantContext = {};
  if (tab) {
    const params: Record<string, string> = {};
    for (const [k, v] of Object.entries(tab.params).slice(0, MAX_PARAMS)) params[k] = clipBytes(String(v), MAX_PARAM_BYTES);
    ctx.tab = { kind: tab.kind, params };
    const subject = subjectOf(tab);
    if (subject) ctx.subject = clipBytes(subject, MAX_PARAM_BYTES);
  }
  const sel = clipBytes(selection, MAX_SELECTION_BYTES);
  if (sel) ctx.selection = sel;
  if (project) ctx.project = project;
  return ctx;
}
