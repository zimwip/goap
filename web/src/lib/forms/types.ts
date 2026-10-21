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
  /** shown, checked and submitted only while every condition holds */
  visibleWhen?: Condition[];
  /** required while every condition holds */
  requiredWhen?: Condition[];
  /** the sub-fields of a group: each entry of its value is an object of their values */
  fields?: FieldDef[];
}

/** A test on the value of an earlier field (pkg/form.Condition): exactly one of equals, in or set. */
export interface Condition {
  field: string;
  equals?: unknown;
  in?: unknown[];
  /** filled in (true) or left blank (false) */
  set?: boolean;
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
  /** the decision points a decision field may rule (a human task: those of the change) */
  decisions?: DecisionOption[];
  /** finds the nodes matching a text beyond `nodes` (a page holding a part of its graph only: the graph searched) */
  search?: (query: string) => Promise<NodeOption[]>;
  /** told of a node picked from a search, so the page holds it from then on (`nodes`) */
  remember?: (n: NodeOption) => void;
}

/** A decision point a decision field may rule. */
export interface DecisionOption {
  id: string;
  question: string;
  /** the options it chooses among (none: a free answer) */
  options: { id: string; label: string }[];
  /** settled: nobody rules it any more */
  decided?: boolean;
}

/** The value of a new node field (one, or a list with multiple). */
export interface NewNodeValue {
  type?: string;
  key?: string;
  props?: Record<string, unknown>;
}

/** The value of a decision field. */
export interface DecisionValue {
  point?: string;
  option?: string;
  justification?: string;
}

/** A file of a file field: its content in base64. */
export interface FileValue {
  name: string;
  type?: string;
  size?: number;
  content: string;
}

export const emptySource: NodeSource = { nodes: [] };

/** A constraint a widget accepts (pkg/form.Constraint). */
export interface ConstraintSpec {
  name: string;
  type: 'string' | 'number' | 'boolean' | 'strings' | 'field' | string;
  description?: string;
  required?: boolean;
  /** a field constraint: the widgets the named field may have */
  widgets?: string[];
}

/** A widget of the server's registry (pkg/form.Widget): its value and constraints. */
export interface WidgetSpec {
  name: string;
  description?: string;
  value: 'string' | 'number' | 'boolean' | 'list' | 'node' | 'links' | 'objects' | 'none' | 'any' | string;
  constraints?: ConstraintSpec[];
}
