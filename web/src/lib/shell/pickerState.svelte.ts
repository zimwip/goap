// Single global "search and pick one" modal, mounted once at the shell root (same pattern as
// fieldDialogState.svelte.ts): a searchable alternative to a plain <select>, for a list that can grow past
// what a dropdown stays usable for (organisation units/users, projects, ...).
export interface PickerItem {
  key: string;
  label: string;
  /** secondary text shown next to the label and matched by the search too (e.g. "(user)", the key itself) */
  hint?: string;
}

export const picker = $state<{
  open: boolean;
  title: string;
  placeholder: string;
  items: PickerItem[];
  onchoose?: (key: string) => void;
}>({ open: false, title: '', placeholder: 'Search…', items: [], onchoose: undefined });

export function openPicker(opts: { title: string; items: PickerItem[]; placeholder?: string; onchoose: (key: string) => void }): void {
  picker.title = opts.title;
  picker.items = opts.items;
  picker.placeholder = opts.placeholder ?? 'Search…';
  picker.onchoose = opts.onchoose;
  picker.open = true;
}

export function closePicker(): void {
  picker.open = false;
}
