// What the Import tab imports (its `what` param): a methodology or a domain definition (YAML or JSON), or a graph
// snapshot (JSON, ADR 0104).

export type ImportKind = 'methodology' | 'domain' | 'graph';

export const IMPORT_KINDS: Record<ImportKind, { title: string; heading: string; accept: string; placeholder: string }> = {
  methodology: {
    title: 'Import methodology',
    heading: 'Import a methodology (YAML or JSON)',
    accept: '.yaml,.yml,.json,text/yaml,application/yaml,application/json',
    placeholder: 'name: …',
  },
  domain: {
    title: 'Import domain',
    heading: 'Import a domain (YAML or JSON)',
    accept: '.yaml,.yml,.json,text/yaml,application/yaml,application/json',
    placeholder: 'name: …',
  },
  graph: {
    title: 'Import graph',
    heading: 'Import a graph snapshot (JSON)',
    accept: '.json,application/json',
    placeholder: '{"format": "goap.graph.snapshot/v1", …}',
  },
};

/** The kind a tab param names; a methodology when it names none or an unknown one. */
export function importKind(what: string | undefined): ImportKind {
  return what === 'domain' || what === 'graph' ? what : 'methodology';
}
