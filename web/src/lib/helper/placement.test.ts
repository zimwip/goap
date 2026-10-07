import { describe, expect, it } from 'vitest';
import { place, visible } from './placement';

const bubble = { width: 300, height: 150 };
const box = (top: number, left: number, w = 200, h = 30) => ({ top, left, right: left + w, bottom: top + h });

describe('place', () => {
  it('goes below the field with the arrow on its middle', () => {
    const p = place(box(100, 50), bubble, 1000, 800);
    expect(p).toMatchObject({ mode: 'anchored', side: 'below', top: 140, left: 50, arrow: 100 });
  });

  it('goes above when there is no room below', () => {
    const p = place(box(700, 50), bubble, 1000, 800);
    expect(p).toMatchObject({ mode: 'anchored', side: 'above', top: 540 });
  });

  it('stays inside the viewport and keeps the arrow on the field', () => {
    const p = place(box(100, 900, 80), bubble, 1000, 800);
    expect(p.mode).toBe('anchored');
    if (p.mode === 'anchored') {
      expect(p.left).toBe(1000 - 300 - 8);
      expect(p.arrow).toBeGreaterThanOrEqual(16);
      expect(p.arrow).toBeLessThanOrEqual(284);
    }
  });

  it('floats when the field is gone, hidden, off screen, or no side has room', () => {
    expect(place(undefined, bubble, 1000, 800)).toEqual({ mode: 'floating' });
    expect(place(box(10, 10, 0, 0), bubble, 1000, 800)).toEqual({ mode: 'floating' });
    expect(place(box(-500, 10), bubble, 1000, 800)).toEqual({ mode: 'floating' });
    expect(place(box(100, 10), bubble, 1000, 200)).toEqual({ mode: 'floating' });
  });

  it('knows what is visible', () => {
    expect(visible(box(10, 10), 1000, 800)).toBe(true);
    expect(visible(box(900, 10), 1000, 800)).toBe(false);
  });
});
