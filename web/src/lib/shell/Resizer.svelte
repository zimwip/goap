<script lang="ts">
  // Poignée de redimensionnement (souris, tactile et clavier).
  let {
    orientation,
    value,
    min,
    max,
    invert = false,
    label,
    onresize,
  }: {
    /** vertical : poignée verticale, redimensionne une largeur */
    orientation: 'vertical' | 'horizontal';
    value: number;
    min: number;
    max: number;
    /** la taille croît quand le pointeur va vers la gauche / le haut */
    invert?: boolean;
    label: string;
    onresize: (v: number) => void;
  } = $props();

  let dragging = $state(false);

  function clamp(v: number) {
    return Math.max(min, Math.min(max, Math.round(v)));
  }

  function down(e: PointerEvent) {
    if (e.button !== 0) return;
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    dragging = true;
    const start = orientation === 'vertical' ? e.clientX : e.clientY;
    const initial = value;
    const move = (ev: PointerEvent) => {
      const pos = orientation === 'vertical' ? ev.clientX : ev.clientY;
      const delta = (pos - start) * (invert ? -1 : 1);
      onresize(clamp(initial + delta));
    };
    const up = () => {
      dragging = false;
      el.removeEventListener('pointermove', move);
      el.removeEventListener('pointerup', up);
      el.removeEventListener('pointercancel', up);
    };
    el.addEventListener('pointermove', move);
    el.addEventListener('pointerup', up);
    el.addEventListener('pointercancel', up);
  }

  function key(e: KeyboardEvent) {
    const step = e.shiftKey ? 48 : 12;
    const grow = orientation === 'vertical' ? (invert ? 'ArrowLeft' : 'ArrowRight') : invert ? 'ArrowUp' : 'ArrowDown';
    const shrink = orientation === 'vertical' ? (invert ? 'ArrowRight' : 'ArrowLeft') : invert ? 'ArrowDown' : 'ArrowUp';
    if (e.key === grow) onresize(clamp(value + step));
    else if (e.key === shrink) onresize(clamp(value - step));
    else if (e.key === 'Home') onresize(min);
    else if (e.key === 'End') onresize(max);
    else return;
    e.preventDefault();
  }
</script>

<!-- Séparateur focalisable (motif « window splitter » WAI-ARIA) : interactif. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
<div
  class="resizer {orientation}"
  class:dragging
  role="separator"
  aria-orientation={orientation}
  aria-label={label}
  aria-valuenow={value}
  aria-valuemin={min}
  aria-valuemax={max}
  tabindex="0"
  onpointerdown={down}
  onkeydown={key}
></div>

<style>
  .resizer {
    position: relative;
    z-index: 3;
    flex: none;
    background: transparent;
    touch-action: none;
  }
  .resizer::after {
    content: '';
    position: absolute;
    background: transparent;
    transition: background 0.1s 0.15s;
  }
  .vertical {
    width: 1px;
    cursor: col-resize;
    background: var(--border);
  }
  .vertical::after {
    inset: 0 -3px;
  }
  .horizontal {
    height: 1px;
    cursor: row-resize;
    background: var(--border);
  }
  .horizontal::after {
    inset: -3px 0;
  }
  .resizer:hover::after,
  .resizer:focus-visible::after,
  .dragging::after {
    background: var(--accent);
  }
  .resizer:focus-visible {
    outline: none;
  }
</style>
