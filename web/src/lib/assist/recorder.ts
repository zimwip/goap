// A tiny recorder of the last thing the person did on the screen, for the `lastAction` of the focus layer (ADR 0092).
// Views call `recordAction` from the paths that change something; the text is a short sentence, never a value.
const WINDOW_MS = 10 * 60 * 1000;
let last: { text: string; at: number } | undefined;

export function recordAction(text: string, now = Date.now()): void {
  const t = text.trim();
  if (t) last = { text: t, at: now };
}

/** The last recorded action when it is recent enough, else ''. */
export function lastAction(now = Date.now()): string {
  return last && now - last.at <= WINDOW_MS ? last.text : '';
}

export function resetRecorder(): void {
  last = undefined;
}
