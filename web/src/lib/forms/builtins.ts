// The built-in widgets of the forms (ADR 0115), registered once on import.
import { registerWidget } from './registry';
import TextWidget from './widgets/TextWidget.svelte';
import NumberWidget from './widgets/NumberWidget.svelte';
import BooleanWidget from './widgets/BooleanWidget.svelte';
import SelectWidget from './widgets/SelectWidget.svelte';
import MultiselectWidget from './widgets/MultiselectWidget.svelte';
import NodeWidget from './widgets/NodeWidget.svelte';
import LinkWidget from './widgets/LinkWidget.svelte';
import MarkdownWidget from './widgets/MarkdownWidget.svelte';

let done = false;

export function registerBuiltinWidgets(): void {
  if (done) return;
  done = true;
  for (const name of ['text', 'textarea', 'date']) registerWidget({ name, component: TextWidget });
  registerWidget({ name: 'number', component: NumberWidget });
  registerWidget({ name: 'boolean', component: BooleanWidget });
  registerWidget({ name: 'select', component: SelectWidget });
  registerWidget({ name: 'multiselect', component: MultiselectWidget });
  registerWidget({ name: 'node', component: NodeWidget });
  registerWidget({ name: 'link', component: LinkWidget });
  registerWidget({ name: 'markdown', component: MarkdownWidget, display: true });
}
