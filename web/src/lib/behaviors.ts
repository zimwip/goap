// Global behaviours of the LLM calls (ADR 0093): the pure logic of the settings pane and the prompt dialog.
import type { LlmBehavior } from './api';

/** The sources a behaviour may be scoped to, as the ledger names them (the server sends its own list). */
export const SOURCES = ['engine', 'assistant', 'helper', 'indexer', 'intent', 'other'];

export const DEFAULT_MAX_INSTRUCTION = 4096;
export const DEFAULT_MAX_TOTAL = 8192;

/** What the form edits. */
export interface BehaviorForm {
  name: string;
  description: string;
  instruction: string;
  enabled: boolean;
  position: 'prepend' | 'append';
  order: number;
  aliases: string[];
  models: string[];
  sources: string[];
  appliesToJson: boolean;
}

export const emptyForm = (): BehaviorForm => ({ name: '', description: '', instruction: '', enabled: false, position: 'append', order: 100, aliases: [], models: [], sources: [], appliesToJson: false });

export const formOf = (b: LlmBehavior): BehaviorForm => ({
  name: b.name,
  description: b.description ?? '',
  instruction: b.instruction ?? '',
  enabled: !!b.enabled,
  position: b.position === 'prepend' ? 'prepend' : 'append',
  order: b.order ?? 0,
  aliases: [...(b.aliases ?? [])],
  models: [...(b.models ?? [])],
  sources: [...(b.sources ?? [])],
  appliesToJson: !!b.appliesToJson,
});

const NAME_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/;

/** UTF-8 length: the cap of the server is in bytes. */
export const byteLength = (s: string) => new TextEncoder().encode(s).length;

/** Why the form cannot be saved, '' when it can. */
export function validateForm(f: BehaviorForm, maxInstruction = DEFAULT_MAX_INSTRUCTION): string {
  if (!NAME_RE.test(f.name)) return 'The name is 1-40 characters of a-z, 0-9, - or _.';
  if (!f.instruction.trim()) return 'The instruction is required.';
  const n = byteLength(f.instruction);
  if (n > maxInstruction) return `The instruction is ${n} bytes, at most ${maxInstruction}.`;
  return '';
}

/** "3 / 4096" style counter, flagged when over the cap. */
export function counter(text: string, max = DEFAULT_MAX_INSTRUCTION): { used: number; max: number; over: boolean } {
  const used = byteLength(text);
  return { used, max, over: used > max };
}

/** Where a behaviour applies, in a few words. */
export function scopeSummary(b: Pick<LlmBehavior, 'aliases' | 'models' | 'sources' | 'appliesToJson'>): string {
  const parts: string[] = [];
  if (b.aliases?.length) parts.push(`aliases ${b.aliases.join(', ')}`);
  if (b.models?.length) parts.push(`models ${b.models.join(', ')}`);
  if (b.sources?.length) parts.push(`from ${b.sources.join(', ')}`);
  const where = parts.length ? parts.join(' · ') : 'every completion';
  return b.appliesToJson ? `${where} · JSON calls too` : where;
}

/** Toggles a value of a list. */
export const toggled = (list: string[], v: string): string[] => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

/** The behaviours of a call, for the prompt dialog and the Tokens console: applied ones, then the ones the cap dropped. */
export function behaviorsLine(names: string[] | undefined, tokens: number | string | undefined): string {
  const applied = (names ?? []).filter((n) => !n.startsWith('!'));
  const dropped = (names ?? []).filter((n) => n.startsWith('!')).map((n) => n.slice(1));
  if (!applied.length && !dropped.length) return '';
  const parts: string[] = [];
  if (applied.length) parts.push(applied.join(', '));
  const t = Number(tokens ?? 0) || 0;
  if (applied.length && t > 0) parts.push(`about ${t} tokens added`);
  if (dropped.length) parts.push(`dropped by the size cap: ${dropped.join(', ')}`);
  return parts.join(' · ');
}
