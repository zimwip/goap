// The widget registry of the forms (ADR 0115): a widget name → the component rendering it. The server holds the other
// half under the same name (pkg/form: its value and constraints, checked by the engine). A page or a plugin adds a
// widget with registerWidget; a field whose widget is not registered is rendered by the JSON fallback.
import type { Component } from 'svelte';
import type { FieldDef, FormValues, NodeSource } from './types';

/** What every widget component receives. */
export interface WidgetProps {
  /** id of the input, for its label */
  id: string;
  field: FieldDef;
  value: unknown;
  onchange: (v: unknown) => void;
  /** the values of the whole form (a link field reads its source from another field) */
  values: FormValues;
  source: NodeSource;
  disabled?: boolean;
  invalid?: boolean;
}

export interface WidgetDef {
  name: string;
  component: Component<WidgetProps>;
  /** a display widget: no label column, no value */
  display?: boolean;
}

const widgets = new Map<string, WidgetDef>();

/** Adds a widget; a second registration of a name replaces the first (a page can specialise a built-in one). */
export function registerWidget(def: WidgetDef): void {
  widgets.set(def.name, def);
}

/** The widget of a name, if registered. */
export const widgetOf = (name: string): WidgetDef | undefined => widgets.get(name);

/** The registered widget names, sorted. */
export const widgetNames = (): string[] => [...widgets.keys()].sort();
