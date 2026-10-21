// What a change waits for from people (ADR 0117): pure helpers over the answer of `GetChangeAsks`. The engine gathers
// the asks (the pending tasks of the runs of the change, the decision points only a person settles), says whom each
// one is addressed to and whether the caller may answer it; the web splits them between the caller and the others.
import type { ChangeAsk } from './api';

export interface AskGroups {
  /** what the caller may answer, the oldest first */
  mine: ChangeAsk[];
  /** what waits for someone else */
  others: ChangeAsk[];
}

/** Splits the asks between what the caller may answer and what waits for others, keeping their order. */
export function groupAsks(asks: ChangeAsk[] | undefined): AskGroups {
  const mine: ChangeAsk[] = [];
  const others: ChangeAsk[] = [];
  for (const a of asks ?? []) (a.mayAnswer ? mine : others).push(a);
  return { mine, others };
}

const KIND: Record<string, string> = {
  input: 'Task',
  approval: 'Approval',
  condition: 'Waiting',
  unblock: 'Stuck run',
  flow: 'Relaunched flow',
  board: 'Blackboard issues',
  merge: 'Merge conflicts',
  rebase: 'Rebase conflicts',
  clarification: 'Clarification',
  question: 'Question',
  decision: 'Decision',
};

/** The label of the kind of an ask. */
export function askKind(a: ChangeAsk): string {
  return KIND[a.kind ?? ''] ?? a.kind ?? 'Ask';
}

/** Readable form of a role name: "release_manager" → "release manager". */
export function roleLabel(role: string): string {
  return role.replace(/[_-]+/g, ' ').trim();
}

/** Whom an ask is addressed to, in words: its roles, else its owner (`you` when it is the caller), else anyone. */
export function addressee(a: ChangeAsk, me = ''): string {
  if (a.roles?.length) return a.roles.map(roleLabel).join(' or ');
  if (a.owner) return me && a.owner === me ? 'you' : a.owner;
  return 'anyone working on the change';
}

/** The asks answered in place in the band: a run's task (its panel), a question (an answer field). A decision is
 * settled in the Decisions pane. */
export function answeredInPlace(a: ChangeAsk): boolean {
  return a.kind === 'question' || (!!a.processId && a.kind !== 'decision');
}

/** How long the change has been waiting, coarse: "just now", "5 min", "3 h", "2 d". */
export function waitingFor(since: string | undefined, now: number = Date.now()): string {
  if (!since) return '';
  const t = Date.parse(since);
  if (Number.isNaN(t)) return '';
  const min = Math.floor((now - t) / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min} min`;
  const h = Math.floor(min / 60);
  if (h < 48) return `${h} h`;
  return `${Math.floor(h / 24)} d`;
}

/** One count of the attention strip of a change (ADR 0152): what it counts, and whether the caller is the one asked. */
export interface AttentionChip {
  label: string;
  mine: boolean;
}

/** The counts the strip shows folded: the asks the caller may answer, the impacts awaiting their review (the caller's to
 * review while the change is open), then the asks waiting for others, grouped by addressee. Empty: nothing waits. */
export function attentionChips(g: AskGroups, reviews: number, me = ''): AttentionChip[] {
  const out: AttentionChip[] = [];
  if (g.mine.length) out.push({ label: `${g.mine.length} for you`, mine: true });
  if (reviews) out.push({ label: `${reviews} impact${reviews > 1 ? 's' : ''} to review`, mine: true });
  const by = new Map<string, number>();
  for (const a of g.others) {
    const who = addressee(a, me);
    by.set(who, (by.get(who) ?? 0) + 1);
  }
  for (const [who, n] of by) out.push({ label: `${n} for ${who}`, mine: false });
  return out;
}
