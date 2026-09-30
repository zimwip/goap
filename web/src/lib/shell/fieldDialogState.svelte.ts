// Small generic "fill in a few fields" dialog: one global instance, opened from context menus (ADR-less, the
// counterpart of objectDialogState.svelte.ts for anything that isn't a graph object).
export interface FieldSpec {
  key: string;
  label: string;
  type?: 'text' | 'textarea';
  required?: boolean;
  placeholder?: string;
  default?: string;
}

export const fieldDialog = $state<{
  open: boolean;
  title: string;
  submitLabel: string;
  fields: FieldSpec[];
  onsubmit?: (values: Record<string, string>) => Promise<void> | void;
}>({ open: false, title: '', submitLabel: 'Create', fields: [], onsubmit: undefined });

export function openFieldDialog(opts: { title: string; submitLabel?: string; fields: FieldSpec[]; onsubmit: (values: Record<string, string>) => Promise<void> | void }): void {
  fieldDialog.title = opts.title;
  fieldDialog.submitLabel = opts.submitLabel ?? 'Create';
  fieldDialog.fields = opts.fields;
  fieldDialog.onsubmit = opts.onsubmit;
  fieldDialog.open = true;
}

export function closeFieldDialog(): void {
  fieldDialog.open = false;
}
