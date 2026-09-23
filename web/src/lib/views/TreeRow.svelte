<script lang="ts">
  // Explorer tree row: single click = preview, double click = pin.
  import type { Snippet } from 'svelte';
  import Icon, { type IconName } from '../shell/Icon.svelte';

  let {
    depth = 0,
    icon,
    label,
    detail = '',
    expanded,
    active = false,
    muted = false,
    italic = false,
    badge,
    badgeTone = 'neutral',
    title,
    onselect,
    onopen,
    ontoggle,
    oncontextmenu,
    actions,
    trail,
  }: {
    depth?: number;
    icon?: IconName;
    label: string;
    detail?: string;
    /** undefined: leaf */
    expanded?: boolean;
    active?: boolean;
    muted?: boolean;
    italic?: boolean;
    badge?: string | number;
    badgeTone?: 'neutral' | 'danger' | 'warn' | 'ok' | 'accent';
    title?: string;
    /** single click */
    onselect?: () => void;
    /** double click / Enter */
    onopen?: () => void;
    ontoggle?: () => void;
    /** right click */
    oncontextmenu?: (e: MouseEvent) => void;
    /** buttons shown on hover */
    actions?: Snippet;
    /** content always shown at the end of the row */
    trail?: Snippet;
  } = $props();

  function click() {
    if (onselect) onselect();
    else ontoggle?.();
  }

  function keydown(e: KeyboardEvent) {
    const row = e.currentTarget as HTMLElement;
    const tree = row.closest('[role=tree]');
    const rows = tree ? [...tree.querySelectorAll<HTMLElement>('[data-tree-row]')] : [];
    const i = rows.indexOf(row);
    if (e.key === 'ArrowDown') rows[i + 1]?.focus();
    else if (e.key === 'ArrowUp') rows[i - 1]?.focus();
    else if (e.key === 'ArrowRight' && expanded === false) ontoggle?.();
    else if (e.key === 'ArrowLeft' && expanded === true) ontoggle?.();
    else if (e.key === 'Enter') (onopen ?? click)();
    else if (e.key === ' ') click();
    else return;
    e.preventDefault();
  }
</script>

<div
  class="trow"
  class:active
  class:muted
  data-tree-row
  role="treeitem"
  aria-expanded={expanded}
  aria-selected={active}
  aria-level={depth + 1}
  tabindex="0"
  title={title ?? (detail ? `${label} — ${detail}` : label)}
  style:padding-left="{6 + depth * 12}px"
  onclick={click}
  ondblclick={() => onopen?.()}
  onkeydown={keydown}
  oncontextmenu={oncontextmenu}
>
  {#if expanded !== undefined}
    <button
      type="button"
      class="twisty"
      tabindex="-1"
      aria-label={expanded ? 'Collapse' : 'Expand'}
      onclick={(e) => {
        e.stopPropagation();
        ontoggle?.();
      }}
    >
      <Icon name={expanded ? 'chevronDown' : 'chevronRight'} size={13} />
    </button>
  {:else}
    <span class="twisty" aria-hidden="true"></span>
  {/if}
  {#if icon}<span class="ic"><Icon name={icon} size={14} /></span>{/if}
  <span class="label" class:italic>{label}</span>
  {#if detail}<span class="detail">{detail}</span>{/if}
  {#if badge !== undefined && badge !== '' && badge !== 0}<span class="badge {badgeTone}">{badge}</span>{/if}
  {#if actions}
    <span class="acts">{@render actions()}</span>
  {/if}
  {#if trail}
    <span class="trail">{@render trail()}</span>
  {/if}
</div>

<style>
  .trow {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    height: var(--row);
    padding-right: 6px;
    cursor: pointer;
    white-space: nowrap;
    user-select: none;
    color: var(--text);
  }
  .trow:hover {
    background: var(--hover);
  }
  .trow.active {
    background: var(--accent-soft);
  }
  .trow:focus-visible {
    outline: 1px solid var(--accent);
    outline-offset: -1px;
  }
  .muted {
    color: var(--muted);
  }
  .twisty {
    width: 16px;
    height: 16px;
    flex: none;
    display: grid;
    place-items: center;
    padding: 0;
    min-height: 0;
    border: none;
    background: transparent;
    color: var(--muted);
  }
  .twisty:hover:not(:disabled) {
    background: transparent;
    color: var(--text);
  }
  .ic {
    color: var(--muted);
  }
  .active .ic {
    color: var(--accent);
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .italic {
    font-style: italic;
  }
  .detail {
    color: var(--muted);
    font-size: 0.88em;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
    min-width: 0;
  }
  .badge {
    margin-left: auto;
    flex: none;
    font-size: 0.75rem;
    font-weight: 700;
    border-radius: 8px;
    padding: 0 5px;
    line-height: 15px;
    background: var(--neutral-soft);
    color: var(--muted);
  }
  .badge.danger {
    background: var(--danger-soft);
    color: var(--danger);
  }
  .badge.warn {
    background: var(--warn-soft);
    color: var(--warn);
  }
  .badge.ok {
    background: var(--ok-soft);
    color: var(--ok);
  }
  .badge.accent {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .acts {
    display: none;
    margin-left: auto;
    gap: 1px;
  }
  .detail + .acts,
  .badge + .acts {
    margin-left: 2px;
  }
  .trow:hover .acts,
  .trow:focus-within .acts {
    display: inline-flex;
  }
  .trail {
    flex: none;
    margin-left: auto;
    display: inline-flex;
    transform: scale(0.88);
    transform-origin: right center;
  }
  .badge + .trail,
  .acts + .trail,
  .detail + .trail {
    margin-left: 2px;
  }
  .acts :global(button) {
    min-height: 0;
    height: 18px;
    width: 18px;
    padding: 0;
    display: grid;
    place-items: center;
    border: none;
    background: transparent;
    color: var(--muted);
  }
  .acts :global(button:hover:not(:disabled)) {
    background: var(--hover);
    color: var(--text);
  }
</style>
