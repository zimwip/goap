// What the person had selected or focused just before they went to the assistant (ADR 0092). Clicking into the
// assistant's input clears the page selection and moves the focus, so both are captured as they happen (and when the
// input takes the focus) and used at send time within a short window. Nothing here is sent unless the collector asks.
const WINDOW_MS = 60 * 1000;
export const MAX_SELECTION_BYTES = 2000;

let selection: { text: string; at: number } | undefined;
let focused: { el: unknown; at: number } | undefined;

/** Remembers a non-empty selection (an empty one leaves the last in place: it is what clicking away does). */
export function recordSelection(text: string, now = Date.now()): void {
  const t = text.trim();
  if (t) selection = { text: t, at: now };
}

/** The last non-empty selection captured within the window ('' otherwise). */
export function capturedSelection(now = Date.now()): string {
  return selection && now - selection.at <= WINDOW_MS ? selection.text : '';
}

export function forgetSelection(): void {
  selection = undefined;
}

export function recordFocus(el: unknown, now = Date.now()): void {
  if (el) focused = { el, at: now };
}

/** The element focused last, when within the window. */
export function capturedFocus(now = Date.now()): unknown {
  return focused && now - focused.at <= WINDOW_MS ? focused.el : undefined;
}

export function resetCapture(): void {
  selection = undefined;
  focused = undefined;
}

const inAssistant = (n: unknown): boolean => !!(n as Element | null)?.closest?.('[data-assistant]');

/** Reads the selection of the page now (called when the assistant's input takes the focus). */
export function captureSelectionNow(): void {
  if (typeof window === 'undefined' || !window.getSelection) return;
  const s = window.getSelection();
  if (s && !inAssistant(s.anchorNode instanceof Element ? s.anchorNode : s.anchorNode?.parentElement)) recordSelection(s.toString());
}

let started = false;
/**
 * Listens to the selection and the focus of the page, outside the assistant; once. `keep` says which focused
 * elements are worth remembering (the registered fields and targets): a click on any other button must not make the
 * assistant forget the field the person was in.
 */
export function startCapture(keep: (el: unknown) => boolean = () => true): void {
  if (started || typeof document === 'undefined') return;
  started = true;
  document.addEventListener('selectionchange', () => {
    if (!inAssistant(document.activeElement)) captureSelectionNow();
  });
  document.addEventListener('focusin', (e) => {
    if (!inAssistant(e.target) && keep(e.target)) recordFocus(e.target);
  });
}
