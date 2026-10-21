// Command ids (ADR 0053). Every request the web makes carries an id (`X-Goap-Command`); the services stamp the
// events a request causes with it, so the client knows the echo of its own writes from what others did.

const MAX_OWN = 500;
const own: string[] = [];
const ownSet = new Set<string>();

/** A new command id, remembered as ours. */
export function newCommandId(): string {
  const id = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  own.push(id);
  ownSet.add(id);
  if (own.length > MAX_OWN) ownSet.delete(own.shift()!);
  return id;
}

/** Did this client issue the command an event carries the id of? */
export function isOwnCommand(id: string): boolean {
  return !!id && ownSet.has(id);
}

export const COMMAND_HEADER = 'X-Goap-Command';

/** the methods of a service that only read: any other is taken for a write */
const READS = /^(Get|List|Diff|Export|Is|Search|Compare|Preview|Check|Watch|Browse)/;

let writes = 0;

/** Notes a call this client made: a write moves `ownWrites` once it answered (failed or not, it may have written). */
export function noteCommand(method: string): void {
  if (!READS.test(method)) writes++;
}

/**
 * How many writes this client made. What it read before one of them may be stale before the event of that write
 * comes back: a cache of reads keys on it, with the stamps of what it read (`headGraph`).
 */
export const ownWrites = (): number => writes;
