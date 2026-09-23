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
  Node: ['id', 'version', 'key', 'type', 'props'],
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
