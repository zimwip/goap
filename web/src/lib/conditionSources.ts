// Where the value of a condition comes from, read from its CEL expression (pkg/condition: the variables of the
// blackboard). A condition that reads the change impacts, the merges, the change or the variables is *dynamic*:
// inferred from the state of the change and of the graph, it can turn false again. One that only reads the facts
// recorded on the blackboard (items, artifacts, decisions) is *set*: it becomes true when an action records that fact.

/** The variables of a condition expression (mirrors condition.Variables). */
export const CONDITION_VARIABLES = ['change', 'items', 'changeImpacts', 'decisions', 'artifacts', 'merges', 'vars'] as const;
const DYNAMIC = new Set(['change', 'changeImpacts', 'merges', 'vars']);

export type ConditionSource = 'dynamic' | 'set' | 'unknown';

/** The blackboard variables an expression reads (string literals and member accesses ignored). */
export function variablesOf(expr: string | undefined): string[] {
  if (!expr) return [];
  const code = expr.replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g, '""');
  const found = new Set<string>();
  for (const m of code.matchAll(/(^|[^.\w])([A-Za-z_]\w*)/g)) {
    const id = m[2];
    if ((CONDITION_VARIABLES as readonly string[]).includes(id)) found.add(id);
  }
  return CONDITION_VARIABLES.filter((v) => found.has(v));
}

export function sourceOf(expr: string | undefined): ConditionSource {
  const vars = variablesOf(expr);
  if (!vars.length) return 'unknown';
  return vars.some((v) => DYNAMIC.has(v)) ? 'dynamic' : 'set';
}
