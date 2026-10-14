// The form of a human action (ADR 0115): what a person fills in, the widgets rendering each field and the constraints
// limiting their choices. The shapes mirror pkg/form.

export interface FormDef {
  title?: string;
  description?: string;
  /** label of the button sending the values */
  submit?: string;
  /** type of the artifact recording the values when the action has no code */
  artifact?: string;
  fields?: FieldDef[];
}

export interface FieldDef {
  name: string;
  widget: string;
  label?: string;
  help?: string;
  placeholder?: string;
  section?: string;
  required?: boolean;
  default?: unknown;
  constraints?: Record<string, unknown>;
}

export type FormValues = Record<string, unknown>;

/** A link a link field asks for. */
export interface LinkValue {
  from?: string;
  type?: string;
  to?: string;
}

/** A node a node or link widget may offer. */
export interface NodeOption {
  key: string;
  type: string;
  title?: string;
  state?: string;
  namespace?: string;
  /** the change touches the node */
  inChange?: boolean;
}

/** What the node and link widgets choose from: the page decides (a human task: the reference baseline of the change
 * and the nodes it writes). */
export interface NodeSource {
  nodes: NodeOption[];
  /** whether a type is the other one or one of its subtypes (default: equality) */
  isA?: (type: string, ancestor: string) => boolean;
  /** qualified link types a node of the type can have as outgoing links (default: none offered) */
  linkTypesFrom?: (type: string) => string[];
  /** label of a link type */
  linkLabel?: (type: string) => string;
}

export const emptySource: NodeSource = { nodes: [] };
