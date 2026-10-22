// Subscriptions of the activities of a methodology and the events it declares (ADR 0110): the events an `on:` or a
// trigger may follow are the standard ones (TRIGGER_EVENTS, the twin of pkg/events) and those the methodology declares.
// The checks are the compiler's (pkg/methodology/event.go), so the editor flags an unknown event before the registry.
import { TRIGGER_EVENTS, type Issue, type Subscription } from './api';

export interface SubscriptionForm {
  event: string;
  filter: string;
  /** "" (self), integrated, integrator, dependency */
  from: string;
  /** an event arriving while the run is at work interrupts it and runs it again */
  supersede: boolean;
}

export interface EventForm {
  name: string;
  description: string;
}

/** A label per standard event. */
export const EVENT_LABELS: Record<string, string> = {
  'change.created': 'change.created — change created',
  'change.applied': 'change.applied — change applied',
  'change.item_added': 'change.item_added — item added to a change',
  'process.completed': 'process.completed — run completed',
  'process.failed': 'process.failed — run failed',
  'process.stuck': 'process.stuck — run stuck (no plan)',
  'process.attached': 'process.attached — run attached to a change',
  'step.completed': 'step.completed — step of a process completed',
  'change.signal': 'change.signal — signal emitted on a change',
  'change.transitioned': 'change.transitioned — change moved in its lifecycle',
  'change.committed': 'change.committed — change committed on its branch',
  'change.merged': 'change.merged — change merged into the branch it was forked from',
  'change.rebased': 'change.rebased — change brought onto the head of the branch it was forked from',
  'change.attached': 'change.attached — change attached to a change integrating it',
  'change.detached': 'change.detached — change detached from the change integrating it',
  'change.edited': 'change.edited — a field of the change header (title, description, goal) was edited',
  'methodology.published': 'methodology.published — methodology published',
};

/** Where the events of a subscription come from (ADR 0106, 0108). */
export const SUBSCRIPTION_ORIGINS = ['', 'integrated', 'integrator', 'dependency'] as const;

/** The form of the name of a declared event (methodology.EventNameRE). */
export const EVENT_NAME_RE = /^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)*$/;

/** The prefixes of the standard events: a declared event takes none of them. */
export const RESERVED_EVENT_FAMILIES = ['change.', 'process.', 'step.', 'methodology.'];

export const isStandardEvent = (name: string): boolean => (TRIGGER_EVENTS as readonly string[]).includes(name);

export function subsToForm(list: Subscription[] | undefined): SubscriptionForm[] {
  return (list ?? []).map((x) => ({ event: x.event ?? '', filter: x.filter ?? '', from: x.from === 'self' ? '' : (x.from ?? ''), supersede: !!x.supersede }));
}

export function subsFromForm(list: SubscriptionForm[]): Subscription[] {
  return list
    .filter((x) => x.event.trim())
    .map((x) => {
      const o: Subscription = { event: x.event.trim() };
      if (x.filter.trim()) o.filter = x.filter.trim();
      if (x.from.trim()) o.from = x.from.trim();
      if (x.supersede) o.supersede = true;
      return o;
    });
}

/** The names of the declared events, trimmed, without the empty ones. */
export function declaredNames(events: EventForm[]): string[] {
  return events.map((e) => e.name.trim()).filter(Boolean);
}

/** Why an event cannot be followed, "" when it can: a standard event or one the methodology declares. */
export function unknownEvent(event: string, declared: string[]): string {
  const e = event.trim();
  if (!e || isStandardEvent(e) || declared.includes(e)) return '';
  return `unknown event "${e}": one of the standard events or an event the methodology declares (events)`;
}

/** The issues of the declared events: the form of a name, a standard prefix, a duplicate. */
export function eventIssues(events: EventForm[]): Issue[] {
  const out: Issue[] = [];
  const seen = new Set<string>();
  events.forEach((e, i) => {
    const name = e.name.trim();
    const path = `events[${i}].name`;
    if (!EVENT_NAME_RE.test(name)) out.push({ path, message: 'an event name is lower case words separated by dots (spec.ready)' });
    else if (RESERVED_EVENT_FAMILIES.some((p) => name.startsWith(p)))
      out.push({ path, message: `"${name}" takes a prefix of the standard events (${RESERVED_EVENT_FAMILIES.join(' ')})` });
    else if (seen.has(name)) out.push({ path, message: `event "${name}" is declared twice` });
    seen.add(name);
  });
  return out;
}

/** The issues of the subscriptions at `path` ("processes[0].steps[1]" → "processes[0].steps[1].on[i].event"). */
export function subscriptionIssues(path: string, subs: SubscriptionForm[], declared: string[]): Issue[] {
  const prefix = path ? `${path}.` : '';
  return subs.flatMap((s, i) => {
    const why = unknownEvent(s.event, declared);
    return why ? [{ path: `${prefix}on[${i}].event`, message: why }] : [];
  });
}

/** The issues of the events an action emits: each a declared event. */
export function emitIssues(path: string, emits: string[], declared: string[]): Issue[] {
  return emits.flatMap((name, i) =>
    declared.includes(name) ? [] : [{ path: `${path}.emits[${i}]`, message: `unknown event "${name}": an action emits an event of the methodology (events)` }],
  );
}
