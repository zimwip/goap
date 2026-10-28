// `ctx` API for script actions (see docs/dsl.md, copied into help/dsl.md):
// powers the code editor's autocompletion.

export type DslGroup = 'read' | 'write' | 'call';

export interface DslFunction {
  /** name (camelCase) */
  name: string;
  args: string[];
  returns: string;
  doc: string;
  group: DslGroup;
}

export const DSL_FUNCTIONS: DslFunction[] = [
  { name: 'intent', args: [], returns: 'string', doc: "Intent expressed at the origin of the process.", group: 'read' },
  { name: 'goal', args: [], returns: 'string', doc: 'Goal being pursued.', group: 'read' },
  { name: 'agent', args: [], returns: 'string', doc: "Agent executing the action.", group: 'read' },
  { name: 'action', args: [], returns: 'string', doc: "Name of the current action.", group: 'read' },
  { name: 'param', args: ['name'], returns: 'JSON value', doc: "Action parameter.", group: 'read' },
  { name: 'var', args: ['name'], returns: 'JSON value', doc: 'Process variable (vars from StartProcess).', group: 'read' },
  { name: 'items', args: ['kind'], returns: 'Item[]', doc: 'Items of the change of this kind ("" = all).', group: 'read' },
  { name: 'options', args: [], returns: 'Option[]', doc: 'Options of the change: hypotheses explored on a flow of their own.', group: 'read' },
  { name: 'decisionPoints', args: [], returns: 'DecisionPoint[]', doc: 'Decision points of the change: questions it must settle.', group: 'read' },
  { name: 'node', args: ['key'], returns: 'Node', doc: 'Node from the reference commit.', group: 'read' },
  { name: 'nodes', args: ['type'], returns: 'Node[]', doc: 'Nodes of this type ("" = all).', group: 'read' },
  {
    name: 'links',
    args: ['key', 'direction', 'type'],
    returns: 'Link[]',
    doc: 'Links of a node; direction "out" / "in", type "" = all.',
    group: 'read',
  },
  { name: 'addArtifact', args: ['type', 'data'], returns: '', doc: 'Free-form data (report…).', group: 'write' },
  { name: 'addItem', args: ['itemType', 'data'], returns: '', doc: 'An item of a type the domains declare (hypothesis, risk, assessment…).', group: 'write' },
  { name: 'changeImpacts', args: [], returns: 'ChangeImpact[]', doc: 'Nodes the change reads, modifies or creates.', group: 'read' },
  { name: 'impactNode', args: ['key', 'rationale'], returns: '"#nN"', doc: 'The change acts on a commit node, and why (no version written yet).', group: 'write' },
  { name: 'impactNodeCreate', args: ['type', 'key', 'rationale'], returns: '"#nN"', doc: 'The change creates a node, and why.', group: 'write' },
  { name: 'impactNodePlan', args: ['type', 'key', 'rationale'], returns: '"#nN"', doc: 'The change will create a node, not written yet (a planned creation, no draft): a later writeNode of its key creates it.', group: 'write' },
  { name: 'impactNodeMerge', args: ['sources', 'type', 'key', 'rationale'], returns: '"#nN"', doc: 'Merge nodes (keys) into a new one, from the side of their parents: they lose the links to the sources and gain one to the new node; the sources stay as they are.', group: 'write' },
  { name: 'impactNodeSplit', args: ['source', '[{type, key, rationale, props}]'], returns: '"#nN"[]', doc: 'Split a node into new ones, from the side of its parents; the node stays as it is.', group: 'write' },
  {
    name: 'writeNode',
    args: ['node', '{props, links, removeLinks}'],
    returns: '',
    doc: 'Edit a node of the change: the first write checks it out (a draft: the node has no version until the change commits), the next ones edit it in place (node: key or #nN; links: [{type, to}]). A state is a transition (impactNodeTransition).',
    group: 'write',
  },
  { name: 'impactNodeReview', args: ['node', 'accept', 'comment'], returns: '', doc: 'Accept or reject a change impact; the comment is mandatory. An acceptance runs the validations on the draft; it is always a separate, explicit action (an edit never reviews).', group: 'write' },
  { name: 'impactNodeReviewWithReserve', args: ['node', 'derogation', 'comment'], returns: '', doc: 'Accept a change impact with a reserve: derogation is the key of an open derogation in force. The review policy still applies (the reviewer is not the producer).', group: 'write' },
  { name: 'openDecision', args: ['question', '{options, criteria, decider, threshold, maxRounds, maxDuration}'], returns: '"#dN"', doc: 'Open a decision point; options are names or ids (none = the open options), every other key is a policy value. Applied after the change impacts.', group: 'write' },
  { name: 'decide', args: ['point', 'option', 'confidence', 'justification'], returns: '', doc: 'Rule a point decided (point: id, #dN, or "" for the only pending one; confidence 0 to 1). Below the point\'s threshold the ruling waits for a person.', group: 'write' },
  { name: 'undecidable', args: ['point', 'justification', 'questions'], returns: '', doc: 'Rule a point undecidable: why, and the questions to answer first (they block it).', group: 'write' },
  { name: 'answer', args: ['questionId', 'answer'], returns: '', doc: 'Answer an open question of a decision point.', group: 'write' },
  { name: 'impactNodeTransition', args: ['node', 'state'], returns: '', doc: 'Move a node along its lifecycle: always on the draft (a node with none is checked out first), never changing the review; authorized and guarded when it is taken.', group: 'write' },
  { name: 'impactNodeCancel', args: ['node'], returns: '', doc: 'Drop the working version of a node (a node the change created and not accepted yet goes away).', group: 'write' },
  { name: 'withdrawImpact', args: ['node'], returns: '', doc: 'Take a node out of the change, at any time (its draft is dropped; a version it committed is reverted by the next commit).', group: 'write' },
  { name: 'llm', args: ['prompt'], returns: 'string', doc: 'Text completion ("default" model).', group: 'call' },
  {
    name: 'complete',
    args: ['{model, system, prompt, json, maxTokens}'],
    returns: '{text, json, inputTokens, outputTokens, model}',
    doc: 'Completion with options.',
    group: 'call',
  },
  {
    name: 'runAgent',
    args: ['name', 'intent'],
    returns: '{status, goal, processId}',
    doc: "Runs a sub-agent on the same change; if the sub-agent waits on a human, the action is suspended then replayed.",
    group: 'call',
  },
  { name: 'callTool', args: ['name', 'args'], returns: 'result', doc: 'MCP tool (server/tool).', group: 'call' },
  { name: 'log', args: ['msg'], returns: '', doc: "Log entry (IDE console).", group: 'call' },
  { name: 'warn', args: ['msg'], returns: '', doc: "Warning (IDE console).", group: 'call' },
];

/** Fields of the returned objects (for completion after `i.` or `n.`). */
export const DSL_FIELDS: Record<string, string[]> = {
  Item: ['id', 'kind', 'type', 'status', 'data', 'producedBy'],
  Node: ['id', 'version', 'key', 'type', 'state', 'props'],
  Link: ['id', 'type', 'from', 'to'],
};

/** Objects the ctx API returns, as the help panel shows them (docs/dsl.md "Reading"). */
export interface DslShape {
  name: string;
  summary: string;
  fields: string[];
}

export const DSL_SHAPES: Record<string, DslShape> = {
  Item: { name: 'Item', summary: 'An artifact or decision recorded on the change.', fields: ['id', 'kind', 'type', 'status', 'data', 'producedBy'] },
  Node: { name: 'Node', summary: 'A graph node: from the reference commit in an action, as it will be after the change in an algorithm.', fields: ['id', 'version', 'key', 'type', 'state', 'props'] },
  Link: { name: 'Link', summary: 'A link between two nodes; from / to are {id, version, key, type}.', fields: ['id', 'type', 'from', 'to'] },
  ChangeImpact: {
    name: 'ChangeImpact',
    summary: 'A node the change reads, modifies or creates. pre / post / committed are Nodes or null; post is the draft (version 0) while the change holds one.',
    fields: ['id', 'key', 'type', 'intent', 'rationale', 'review', 'planned', 'drafted', 'pre', 'post', 'committed', 'links'],
  },
  Option: {
    name: 'Option',
    summary: 'A hypothesis explored on a flow of its own; status is exploring, evaluated, selected or rejected.',
    fields: ['id', 'name', 'hypothesis', 'status', 'active', 'evaluation'],
  },
  DecisionPoint: {
    name: 'DecisionPoint',
    summary: 'A question the change must settle; status is open, blocked, ratifying, escalated or decided.',
    fields: ['id', 'question', 'options', 'criteria', 'policy', 'status', 'questions', 'option'],
  },
  Change: { name: 'Change', summary: 'The change being applied.', fields: ['id', 'title', 'intent', 'methodology', 'goal'] },
  Transition: { name: 'Transition', summary: 'The lifecycle transition being taken.', fields: ['name', 'from', 'to'] },
};

/** Script language of actions, algorithms and adapters: JavaScript is the only one (a stored script naming
 * another language, an old Go one, is read as JavaScript). */
export const SCRIPT_LANGUAGE = 'javascript';

/** Starter script of an action of kind script. */
export const ACTION_TEMPLATE = `// Script actions: the ctx object gives access to the blackboard (see "DSL Help").
for (const n of ctx.changeImpacts()) {
  ctx.log("change impact " + n.key);
}
`;

// --- algorithms (ADR 0018) -----------------------------------------------------------
// Algorithms plug into the model at fixed extension points. Each type runs in its own
// `ctx`: an algorithm (JavaScript) is the BODY of a function of ctx (it may `return`). They are pure: no blackboard, LLM or tool calls.

export type AlgorithmUsage = 'property_validator' | 'node_validator' | 'link_validator' | 'transition_guard' | 'transition_action' | 'adapter';

export interface AlgorithmUsageInfo {
  usage: AlgorithmUsage;
  title: string;
  description: string;
  /** what the plug does with the result */
  contract: string;
  functions: DslFunction[];
  /** sample input of "Try it" */
  sample: Record<string, unknown>;
}

const paramFn: DslFunction = { name: 'param', args: ['name'], returns: 'JSON value', doc: 'Parameter value set by the instance (defaults filled in).', group: 'read' };
const failFn: DslFunction = { name: 'fail', args: ['message'], returns: '', doc: 'Reject with a message (may be called several times).', group: 'call' };
const logFns: DslFunction[] = [
  { name: 'log', args: ['msg'], returns: '', doc: 'Log entry.', group: 'call' },
  { name: 'warn', args: ['msg'], returns: '', doc: 'Warning.', group: 'call' },
];
const metaFns: DslFunction[] = [
  { name: 'instance', args: [], returns: 'string', doc: 'Name of the algorithm instance being run.', group: 'read' },
  { name: 'algorithm', args: [], returns: 'string', doc: 'Name of the algorithm.', group: 'read' },
];
const nodeFn: DslFunction = { name: 'node', args: [], returns: 'Node', doc: 'The node concerned: {id, version, key, type, state, props}.', group: 'read' };
const impactFn: DslFunction = { name: 'impact', args: [], returns: '{intent, review}', doc: 'The impact of the node in the change: why it is in it and the state of its review (a transition that requires a review checks it).', group: 'read' };
const groupFns: DslFunction[] = [
  { name: 'children', args: [], returns: 'Node[]', doc: 'Nodes a document contains, as they are in the result of the change.', group: 'read' },
  { name: 'change', args: [], returns: '{id, title, intent, methodology, goal}', doc: 'The change being applied.', group: 'read' },
  { name: 'transition', args: [], returns: '{name, from, to}', doc: 'The lifecycle transition.', group: 'read' },
];

export const ALGORITHM_USAGES: AlgorithmUsageInfo[] = [
  {
    usage: 'property_validator',
    title: 'Property validator',
    description: 'Checks the value of a property when a node of the type is created or modified.',
    contract: 'Rejects with ctx.fail(msg), a throw, or by returning false / a message.',
    functions: [
      paramFn,
      { name: 'property', args: [], returns: 'string', doc: 'Name of the property being validated.', group: 'read' },
      { name: 'value', args: [], returns: 'JSON value', doc: 'Value of the property (null when the node has none).', group: 'read' },
      nodeFn,
      failFn,
      ...metaFns,
      ...logFns,
    ],
    sample: { property: 'title', value: 'Card payment', node: { key: 'REQ-1', type: 'Requirement', state: 'draft', props: { title: 'Card payment' } } },
  },
  {
    usage: 'node_validator',
    title: 'Node validator',
    description: 'Checks a node as a whole (rules across several attributes) when a node of the type is created or modified, after the attribute validators.',
    contract: 'Rejects with ctx.fail(msg), a throw, or by returning false / a message.',
    functions: [paramFn, nodeFn, failFn, ...metaFns, ...logFns],
    sample: { node: { key: 'REQ-1', type: 'Requirement', state: 'draft', props: { title: 'Card payment', start: '2026-01-01', end: '2026-02-01' } } },
  },
  {
    usage: 'link_validator',
    title: 'Link validator',
    description: 'Checks a link as a whole, with the node it leaves and the node it reaches, when the draft is accepted and at commit, after the validators of the link attributes.',
    contract: 'Rejects with ctx.fail(msg), a throw, or by returning false / a message.',
    functions: [
      paramFn,
      { name: 'link', args: [], returns: '{type, props}', doc: 'The link checked: its type and properties.', group: 'read' },
      { name: 'from', args: [], returns: 'Node', doc: 'The node the link leaves, as it will be.', group: 'read' },
      { name: 'to', args: [], returns: 'Node', doc: 'The node the link reaches, as it will be.', group: 'read' },
      failFn,
      ...metaFns,
      ...logFns,
    ],
    sample: {
      link: { type: 'refines', props: {} },
      from: { key: 'REQ-1', type: 'Requirement', state: 'draft', props: { title: 'Card payment' } },
      to: { key: 'REQ-2', type: 'Requirement', state: 'approved', props: { title: 'Payment' } },
    },
  },
  {
    usage: 'transition_guard',
    title: 'Transition guard',
    description: 'Decides whether a lifecycle transition is allowed; checked when the change is applied, after the CEL guard.',
    contract: 'Refuses with ctx.fail(msg), a throw / returned error, or by returning false / a message (JavaScript).',
    functions: [paramFn, nodeFn, ...groupFns, impactFn, failFn, ...metaFns, ...logFns],
    sample: {
      node: { key: 'SPEC-1', type: 'Spec', state: 'released', props: { title: 'Spec' } },
      children: [{ key: 'REQ-1', type: 'Requirement', state: 'approved', props: {} }, { key: 'REQ-2', type: 'Requirement', state: 'draft', props: {} }],
      transition: { name: 'release', from: 'draft', to: 'released' },
      change: { id: 'c1', title: 'Release the spec' },
    },
  },
  {
    usage: 'transition_action',
    title: 'Transition action',
    description: 'Runs once a transition is accepted and may change properties of the node in the version it produces.',
    contract: 'Changes properties with ctx.setProp / ctx.removeProp; ctx.fail(msg) or a throw aborts the application of the change.',
    functions: [
      paramFn,
      nodeFn,
      ...groupFns,
      { name: 'setProp', args: ['name', 'value'], returns: '', doc: 'Set a property of the node.', group: 'write' },
      { name: 'removeProp', args: ['name'], returns: '', doc: 'Remove a property of the node.', group: 'write' },
      failFn,
      ...metaFns,
      ...logFns,
    ],
    sample: {
      node: { key: 'REQ-1', type: 'Requirement', state: 'approved', props: { title: 'Card payment' } },
      transition: { name: 'approve', from: 'in_review', to: 'approved' },
      change: { id: 'c1', title: 'Approve REQ-1' },
    },
  },
  {
    usage: 'adapter',
    title: 'Adapter',
    description:
      'Implements the tools an MCP expects with the operations a connector exposes. Declared in the library, instantiated by organisational units with their parameter values; never plugged into a node type or a lifecycle.',
    contract:
      'Returns the result of the tool (an object, or any value, wrapped); rejects with ctx.fail(msg) or a throw. A failing ctx.call throws.',
    functions: [
      { name: 'tool', args: [], returns: 'string', doc: 'Name of the MCP tool being called.', group: 'read' },
      { name: 'args', args: [], returns: 'object', doc: 'Arguments of the tool call.', group: 'read' },
      { name: 'param', args: ['name'], returns: 'JSON value', doc: 'Parameter value of the unit instance (secrets are not readable).', group: 'read' },
      { name: 'operations', args: [], returns: 'string[]', doc: 'Operations the connector exposes.', group: 'read' },
      { name: 'call', args: ['operation', 'args'], returns: 'object', doc: 'Run an operation of the connector (the hub adds the parameters as its configuration and the secrets); throws on failure. At most 32 calls per execution.', group: 'call' },
      failFn,
      ...logFns,
    ],
    sample: {},
  },
];

export function algorithmUsage(usage: string): AlgorithmUsageInfo | undefined {
  return ALGORITHM_USAGES.find((u) => u.usage === usage);
}

export const ALGORITHM_TEMPLATES: Record<AlgorithmUsage, string> = {
  property_validator: `// Body of a function of ctx. Reject with ctx.fail(message) or by returning a message.
const v = ctx.value();
if (typeof v !== "string" || v.trim() === "") {
  ctx.fail(ctx.property() + " must not be blank");
}
`,
  node_validator: `// Body of a function of ctx. Reject with ctx.fail(message) or by returning a message.
const p = ctx.node().props;
if (p.start && p.end && p.end < p.start) {
  ctx.fail("end must not be before start");
}
`,
  link_validator: `// Body of a function of ctx. Reject with ctx.fail(message) or by returning a message.
if (ctx.from().state === "approved" && ctx.to().state === "draft") {
  ctx.fail(ctx.from().key + " is approved but refines the draft " + ctx.to().key);
}
`,
  transition_guard: `// Body of a function of ctx. Refuse with ctx.fail(message).
for (const c of ctx.children()) {
  if (c.state !== "approved") ctx.fail(c.key + " is " + c.state);
}
`,
  adapter: `// Body of a function of ctx: ctx.tool() is the MCP tool called, ctx.args() its arguments,
// ctx.call(operation, args) runs an operation of the connector. Return the result of the tool.
switch (ctx.tool()) {
  // case "read": return ctx.call("read_file", { path: ctx.args().path });
}
ctx.fail("unknown tool " + ctx.tool());
`,
  transition_action: `// Body of a function of ctx. Change the node with ctx.setProp / ctx.removeProp.
ctx.setProp("lastTransition", ctx.transition().name);
`,
};
