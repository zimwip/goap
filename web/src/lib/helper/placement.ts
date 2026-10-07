// Where the helper's bubble goes relative to its field: below the field, or above when there is no room below,
// with an arrow pointing at the field; a floating panel when the field is not visible or no side has room.

export interface Box {
  top: number;
  left: number;
  right: number;
  bottom: number;
}

export type Placement =
  | { mode: 'anchored'; side: 'below' | 'above'; top: number; left: number; arrow: number }
  | { mode: 'floating' };

const GAP = 10;
const MARGIN = 8;
const ARROW_MIN = 16;

/** Is any part of the box inside the viewport (and does it have a size)? */
export function visible(r: Box, vw: number, vh: number): boolean {
  return r.right - r.left > 0 && r.bottom - r.top > 0 && r.bottom > 0 && r.top < vh && r.right > 0 && r.left < vw;
}

export function place(target: Box | undefined, bubble: { width: number; height: number }, vw: number, vh: number): Placement {
  if (!target || !visible(target, vw, vh)) return { mode: 'floating' };
  let side: 'below' | 'above';
  let top: number;
  if (target.bottom + GAP + bubble.height <= vh - MARGIN) {
    side = 'below';
    top = target.bottom + GAP;
  } else if (target.top - GAP - bubble.height >= MARGIN) {
    side = 'above';
    top = target.top - GAP - bubble.height;
  } else return { mode: 'floating' };
  const left = Math.max(MARGIN, Math.min(target.left, vw - bubble.width - MARGIN));
  const center = (Math.max(target.left, 0) + Math.min(target.right, vw)) / 2;
  const arrow = Math.max(ARROW_MIN, Math.min(center - left, bubble.width - ARROW_MIN));
  return { mode: 'anchored', side, top, left, arrow };
}
