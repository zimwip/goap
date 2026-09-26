// `ctx` API for script actions (see docs/dsl.md, copied into help/dsl.md):
// powers the code editor's autocompletion.

export type DslGroup = 'read' | 'write' | 'call';

export interface DslFunction {
  /** JavaScript name (camelCase); the Go name is PascalCase */
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
  { name: 'impacts', args: [], returns: 'Item[]', doc: 'Impacts of the change.', group: 'read' },
  { name: 'proposals', args: [], returns: 'Item[]', doc: 'Proposals of the change.', group: 'read' },
  { name: 'node', args: ['key'], returns: 'Node', doc: 'Node from the reference baseline.', group: 'read' },
  { name: 'nodes', args: ['type'], returns: 'Node[]', doc: 'Nodes of this type ("" = all).', group: 'read' },
  {
    name: 'links',
    args: ['key', 'direction', 'type'],
    returns: 'Link[]',
    doc: 'Links of a node; direction "out" / "in", type "" = all.',
    group: 'read',
  },
  { name: 'addImpact', args: ['key', 'reason'], returns: '', doc: 'Direct impact on a baseline node.', group: 'write' },
  {
    name: 'proposeNode',
    args: ['type', 'key', 'props'],
    returns: '"#pN"',
    doc: 'Proposal to create a node; returns a #pN reference.',
    group: 'write',
  },
  { name: 'proposeUpdate', args: ['key', 'props'], returns: '', doc: "New version of a node.", group: 'write' },
  { name: 'proposeDelete', args: ['key'], returns: '', doc: "Deletion of a node.", group: 'write' },
  {
    name: 'proposeLink',
    args: ['from', 'type', 'to'],
    returns: '',
    doc: 'Link between two nodes (node key or #pN reference).',
    group: 'write',
  },
  { name: 'addArtifact', args: ['type', 'data'], returns: '', doc: 'Free-form data (report…).', group: 'write' },
  { name: 'decide', args: ['itemId', 'accept', 'comment'], returns: '', doc: 'Decision on a proposal.', group: 'write' },
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
  Item: ['id', 'kind', 'type', 'status', 'target', 'data', 'op', 'node', 'producedBy'],
  Node: ['id', 'version', 'key', 'type', 'state', 'props'],
  Link: ['id', 'type', 'from', 'to'],
};

export function goName(name: string): string {
  return name.charAt(0).toUpperCase() + name.slice(1);
}

export const TEMPLATES: Record<string, string> = {
  javascript: `// Script actions: the ctx object gives access to the blackboard (see "DSL Help").
for (const i of ctx.impacts()) {
  ctx.log("impact on " + i.target.key);
}
`,
  go: `package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.Ctx) error {
\tfor _, i := range ctx.Impacts() {
\t\tctx.Log("impact on " + i.Target.Key)
\t}
\treturn nil
}
`,
};

// --- algorithms (ADR 0018) -----------------------------------------------------------
// Algorithms plug into the model at fixed extension points. Each type runs in its own
// `ctx`: a JavaScript algorithm is the BODY of a function of ctx (it may `return`), a Go
// algorithm declares `func Run(ctx *dsl.<Ctx>) error`. They are pure: no blackboard, LLM or tool calls.

export type AlgorithmUsage = 'property_validator' | 'transition_guard' | 'transition_action';

export interface AlgorithmUsageInfo {
  usage: AlgorithmUsage;
  title: string;
  /** Go context type */
  goCtx: string;
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
const groupFns: DslFunction[] = [
  { name: 'children', args: [], returns: 'Node[]', doc: 'Nodes a document contains, as they are in the result of the change.', group: 'read' },
  { name: 'change', args: [], returns: '{id, title, intent, methodology, goal}', doc: 'The change being applied.', group: 'read' },
  { name: 'transition', args: [], returns: '{name, from, to}', doc: 'The lifecycle transition.', group: 'read' },
];

export const ALGORITHM_USAGES: AlgorithmUsageInfo[] = [
  {
    usage: 'property_validator',
    title: 'Property validator',
    goCtx: 'ValidatorCtx',
    description: 'Checks the value of a property when a node of the type is created or modified.',
    contract: 'Rejects with ctx.fail(msg), a throw (Go: a returned error), or by returning false / a message (JavaScript).',
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
    usage: 'transition_guard',
    title: 'Transition guard',
    goCtx: 'GuardCtx',
    description: 'Decides whether a lifecycle transition is allowed; checked when the change is applied, after the CEL guard.',
    contract: 'Refuses with ctx.fail(msg), a throw / returned error, or by returning false / a message (JavaScript).',
    functions: [paramFn, nodeFn, ...groupFns, failFn, ...metaFns, ...logFns],
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
    goCtx: 'TransitionCtx',
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
];

export function algorithmUsage(usage: string): AlgorithmUsageInfo | undefined {
  return ALGORITHM_USAGES.find((u) => u.usage === usage);
}

export const ALGORITHM_TEMPLATES: Record<AlgorithmUsage, Record<'javascript' | 'go', string>> = {
  property_validator: {
    javascript: `// Body of a function of ctx. Reject with ctx.fail(message) or by returning a message.
const v = ctx.value();
if (typeof v !== "string" || v.trim() === "") {
  ctx.fail(ctx.property() + " must not be blank");
}
`,
    go: `package validator

import (
\t"strings"

\t"github.com/zimwip/goap/pkg/dsl"
)

func Run(ctx *dsl.ValidatorCtx) error {
\ts, _ := ctx.Value().(string)
\tif strings.TrimSpace(s) == "" {
\t\tctx.Fail(ctx.Property() + " must not be blank")
\t}
\treturn nil
}
`,
  },
  transition_guard: {
    javascript: `// Body of a function of ctx. Refuse with ctx.fail(message).
for (const c of ctx.children()) {
  if (c.state !== "approved") ctx.fail(c.key + " is " + c.state);
}
`,
    go: `package guard

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.GuardCtx) error {
\tfor _, c := range ctx.Children() {
\t\tif c.State != "approved" {
\t\t\tctx.Fail(c.Key + " is " + c.State)
\t\t}
\t}
\treturn nil
}
`,
  },
  transition_action: {
    javascript: `// Body of a function of ctx. Change the node with ctx.setProp / ctx.removeProp.
ctx.setProp("lastTransition", ctx.transition().name);
`,
    go: `package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.TransitionCtx) error {
\tctx.SetProp("lastTransition", ctx.Transition().Name)
\treturn nil
}
`,
  },
};
