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
