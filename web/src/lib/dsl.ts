// API `ctx` des actions script (voir docs/dsl.md, copiée dans help/dsl.md) :
// alimente l'autocomplétion de l'éditeur de code.

export type DslGroup = 'lecture' | 'écriture' | 'appel';

export interface DslFunction {
  /** nom JavaScript (camelCase) ; le nom Go est en PascalCase */
  name: string;
  args: string[];
  returns: string;
  doc: string;
  group: DslGroup;
}

export const DSL_FUNCTIONS: DslFunction[] = [
  { name: 'intent', args: [], returns: 'string', doc: "Intention exprimée à l'origine du processus.", group: 'lecture' },
  { name: 'goal', args: [], returns: 'string', doc: 'Objectif poursuivi.', group: 'lecture' },
  { name: 'agent', args: [], returns: 'string', doc: "Agent qui exécute l'action.", group: 'lecture' },
  { name: 'action', args: [], returns: 'string', doc: "Nom de l'action en cours.", group: 'lecture' },
  { name: 'param', args: ['name'], returns: 'valeur JSON', doc: "Paramètre de l'action.", group: 'lecture' },
  { name: 'var', args: ['name'], returns: 'valeur JSON', doc: 'Variable du processus (vars de StartProcess).', group: 'lecture' },
  { name: 'items', args: ['kind'], returns: 'Item[]', doc: 'Items du changement de ce type ("" = tous).', group: 'lecture' },
  { name: 'impacts', args: [], returns: 'Item[]', doc: 'Impacts du changement.', group: 'lecture' },
  { name: 'proposals', args: [], returns: 'Item[]', doc: 'Propositions du changement.', group: 'lecture' },
  { name: 'node', args: ['key'], returns: 'Node', doc: 'Nœud de la baseline de référence.', group: 'lecture' },
  { name: 'nodes', args: ['type'], returns: 'Node[]', doc: 'Nœuds de ce type ("" = tous).', group: 'lecture' },
  {
    name: 'links',
    args: ['key', 'direction', 'type'],
    returns: 'Link[]',
    doc: 'Liens d\'un nœud ; direction "out" / "in", type "" = tous.',
    group: 'lecture',
  },
  { name: 'addImpact', args: ['key', 'reason'], returns: '', doc: 'Impact direct sur un nœud de la baseline.', group: 'écriture' },
  {
    name: 'proposeNode',
    args: ['type', 'key', 'props'],
    returns: '"#pN"',
    doc: 'Proposition de création de nœud ; renvoie une référence #pN.',
    group: 'écriture',
  },
  { name: 'proposeUpdate', args: ['key', 'props'], returns: '', doc: "Nouvelle version d'un nœud.", group: 'écriture' },
  { name: 'proposeDelete', args: ['key'], returns: '', doc: "Suppression d'un nœud.", group: 'écriture' },
  {
    name: 'proposeLink',
    args: ['from', 'type', 'to'],
    returns: '',
    doc: 'Lien entre deux nœuds (clé de nœud ou référence #pN).',
    group: 'écriture',
  },
  { name: 'addArtifact', args: ['type', 'data'], returns: '', doc: 'Donnée libre (rapport…).', group: 'écriture' },
  { name: 'decide', args: ['itemId', 'accept', 'comment'], returns: '', doc: 'Décision sur une proposition.', group: 'écriture' },
  { name: 'llm', args: ['prompt'], returns: 'string', doc: 'Complétion texte (modèle « default »).', group: 'appel' },
  {
    name: 'complete',
    args: ['{model, system, prompt, json, maxTokens}'],
    returns: '{text, json, inputTokens, outputTokens, model}',
    doc: 'Complétion avec options.',
    group: 'appel',
  },
  {
    name: 'runAgent',
    args: ['name', 'intent'],
    returns: '{status, goal, processId}',
    doc: "Exécute un sous-agent sur le même change ; si le sous-agent attend un humain, l'action est suspendue puis rejouée.",
    group: 'appel',
  },
  { name: 'callTool', args: ['name', 'args'], returns: 'résultat', doc: 'Outil MCP (serveur/outil).', group: 'appel' },
  { name: 'log', args: ['msg'], returns: '', doc: "Journal (console de l'IDE).", group: 'appel' },
  { name: 'warn', args: ['msg'], returns: '', doc: "Avertissement (console de l'IDE).", group: 'appel' },
];

/** Champs des objets renvoyés (pour la complétion après `i.` ou `n.`). */
export const DSL_FIELDS: Record<string, string[]> = {
  Item: ['id', 'kind', 'type', 'status', 'target', 'data', 'op', 'node', 'producedBy'],
  Node: ['id', 'version', 'key', 'type', 'props'],
  Link: ['id', 'type', 'from', 'to'],
};

export function goName(name: string): string {
  return name.charAt(0).toUpperCase() + name.slice(1);
}

export const TEMPLATES: Record<string, string> = {
  javascript: `// Actions script : l'objet ctx donne accès au blackboard (voir « Aide DSL »).
for (const i of ctx.impacts()) {
  ctx.log("impact sur " + i.target.key);
}
`,
  go: `package action

import "github.com/zimwip/goap/pkg/dsl"

func Run(ctx *dsl.Ctx) error {
\tfor _, i := range ctx.Impacts() {
\t\tctx.Log("impact sur " + i.Target.Key)
\t}
\treturn nil
}
`,
};
