// État « déplié » des arbres des explorateurs, persisté.
import { loadRaw, save } from '../../shell/storage';

const KEY = 'goap.ide.expanded';

const initial = loadRaw(KEY);
export const expanded: Record<string, boolean> = $state(
  initial && typeof initial === 'object' ? (initial as Record<string, boolean>) : {},
);

$effect.root(() => {
  $effect(() => {
    save(KEY, { ...expanded });
  });
});

export function toggle(key: string, def = false): void {
  expanded[key] = !(expanded[key] ?? def);
}

export function isOpen(key: string, def = false): boolean {
  return expanded[key] ?? def;
}
