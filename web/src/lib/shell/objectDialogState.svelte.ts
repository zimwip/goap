// "New object" dialog: creates a data node typed by a node type of a
// published methodology. One global dialog, opened from context menus.
export const objectDialog = $state<{ open: boolean; methodology: string; nodeType: string; properties: string[] }>({
  open: false,
  methodology: '',
  nodeType: '',
  properties: [],
});

export function openObjectDialog(methodology: string, nodeType: string, properties: string[]): void {
  objectDialog.methodology = methodology;
  objectDialog.nodeType = nodeType;
  objectDialog.properties = properties;
  objectDialog.open = true;
}

export function closeObjectDialog(): void {
  objectDialog.open = false;
}
