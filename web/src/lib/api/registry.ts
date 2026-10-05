import { rpc } from './transport';
import type { Empty } from './types/common';
import type { Algorithm, Domain, DomainSummary, DomainUser, Issue, LevelCheck, LinkTypeInfo, Methodology, MethodologySummary, PlanPreview, ProcessGraph, RunAlgorithmResponse, TypeInfo } from './types/registry';

const REGISTRY = 'goap.registry.v1.RegistryService';
type NameVersion = { name: string; version: string };

export const registry = {
  /** `allVersions`: all versions (drafts, archived) instead of the latest by name. */
  listMethodologies: (allVersions = false, signal?: AbortSignal) =>
    rpc<{ allVersions?: boolean }, { methodologies?: MethodologySummary[] }>(
      REGISTRY,
      'ListMethodologies',
      allVersions ? { allVersions } : {},
      signal,
    ),
  /** empty `version`: latest published version. */
  getMethodology: (name: string, version = '', signal?: AbortSignal) =>
    rpc<NameVersion, { methodology?: Methodology }>(REGISTRY, 'GetMethodology', { name, version }, signal),
  saveMethodology: (methodology: Methodology) =>
    rpc<{ methodology: Methodology }, { methodology?: Methodology; issues?: Issue[] }>(REGISTRY, 'SaveMethodology', {
      methodology,
    }),
  validateMethodology: (methodology: Methodology) =>
    rpc<{ methodology: Methodology }, { issues?: Issue[] }>(REGISTRY, 'ValidateMethodology', { methodology }),
  /** a process of a methodology as edited, as a graph (ADR 0036 §4) */
  processGraph: (methodology: Methodology, process: string, signal?: AbortSignal) =>
    rpc<{ methodology: Methodology; process: string }, { graph?: ProcessGraph; issues?: Issue[] }>(REGISTRY, 'GetProcessGraph', { methodology, process }, signal),
  /** coherence of a process or method, level by level, as edited */
  checkLevels: (methodology: Methodology, root: string, signal?: AbortSignal) =>
    rpc<{ methodology: Methodology; root: string }, { levels?: LevelCheck[]; issues?: Issue[]; conditions?: Record<string, string> }>(REGISTRY, 'CheckLevels', { methodology, root }, signal),
  /**
   * Plans toward `goal` with the planner `agent` is actually configured with, from an empty blackboard whose
   * evaluated conditions `overrides` patch on top: no live Change needed. For a process, pass its name as both
   * agent and goal; for a step naming an agent or a capability, pass the step's (or chosen method's) agent/goal.
   */
  previewPlan: (methodology: Methodology, agent: string, goal: string, overrides: Record<string, boolean>, signal?: AbortSignal) =>
    rpc<{ methodology: Methodology; agent: string; goal: string; overrides: Record<string, boolean> }, { preview?: PlanPreview; issues?: Issue[] }>(
      REGISTRY,
      'PreviewPlan',
      { methodology, agent, goal, overrides },
      signal,
    ),
  publishMethodology: (name: string, version: string) =>
    rpc<NameVersion, { methodology?: Methodology }>(REGISTRY, 'PublishMethodology', { name, version }),
  createVersion: (name: string, fromVersion: string, newVersion: string) =>
    rpc<{ name: string; fromVersion: string; newVersion: string }, { methodology?: Methodology }>(
      REGISTRY,
      'CreateVersion',
      { name, fromVersion, newVersion },
    ),
  /** Deletes a draft, or archives a published version. */
  deleteMethodology: (name: string, version: string) =>
    rpc<NameVersion, Empty>(REGISTRY, 'DeleteMethodology', { name, version }),
  importMethodology: (yaml: string, publish: boolean) =>
    rpc<{ yaml: string; publish?: boolean }, { methodology?: Methodology; issues?: Issue[] }>(
      REGISTRY,
      'ImportMethodology',
      publish ? { yaml, publish } : { yaml },
    ),
  exportMethodology: (name: string, version: string) =>
    rpc<NameVersion, { yaml?: string; filename?: string }>(REGISTRY, 'ExportMethodology', { name, version }),

  // --- domains (one per namespace; no change / impact / proposal involved) ---
  listDomains: (allVersions = false, signal?: AbortSignal) =>
    rpc<{ allVersions?: boolean }, { domains?: DomainSummary[] }>(
      REGISTRY,
      'ListDomains',
      allVersions ? { allVersions } : {},
      signal,
    ),
  /** empty `version`: latest published version. */
  getDomain: (name: string, version = '', signal?: AbortSignal) =>
    rpc<NameVersion, { domain?: Domain }>(REGISTRY, 'GetDomain', { name, version }, signal),
  saveDomain: (domain: Domain) =>
    rpc<{ domain: Domain }, { domain?: Domain; issues?: Issue[] }>(REGISTRY, 'SaveDomain', { domain }),
  validateDomain: (domain: Domain) => rpc<{ domain: Domain }, { issues?: Issue[] }>(REGISTRY, 'ValidateDomain', { domain }),
  publishDomain: (name: string, version: string) =>
    rpc<NameVersion, { domain?: Domain }>(REGISTRY, 'PublishDomain', { name, version }),
  createDomainVersion: (name: string, fromVersion: string, newVersion: string) =>
    rpc<{ name: string; fromVersion: string; newVersion: string }, { domain?: Domain }>(REGISTRY, 'CreateDomainVersion', {
      name,
      fromVersion,
      newVersion,
    }),
  /** Deletes a draft, or archives a published version. */
  deleteDomain: (name: string, version: string) => rpc<NameVersion, Empty>(REGISTRY, 'DeleteDomain', { name, version }),
  importDomain: (yaml: string, publish: boolean) =>
    rpc<{ yaml: string; publish?: boolean }, { domain?: Domain; issues?: Issue[] }>(
      REGISTRY,
      'ImportDomain',
      publish ? { yaml, publish } : { yaml },
    ),
  exportDomain: (name: string, version: string) =>
    rpc<NameVersion, { yaml?: string; filename?: string }>(REGISTRY, 'ExportDomain', { name, version }),
  /** Methodology versions referencing a domain version (unpinned references included). */
  getDomainUsage: (name: string, version: string, signal?: AbortSignal) =>
    rpc<NameVersion, { methodologies?: DomainUser[] }>(REGISTRY, 'GetDomainUsage', { name, version }, signal),
  /** The type catalogue in force (ADR 0012): node and link types of the published and built-in domains. */
  listTypes: (signal?: AbortSignal) =>
    rpc<Record<string, never>, { types?: TypeInfo[]; linkTypes?: LinkTypeInfo[]; domains?: Record<string, string> }>(REGISTRY, 'ListTypes', {}, signal),
  /** Tries an algorithm on a sample input; nothing is saved. */
  runAlgorithm: (algorithm: Algorithm, values: Record<string, unknown>, input: Record<string, unknown>) =>
    rpc<{ algorithm: Algorithm; values: Record<string, unknown>; input: Record<string, unknown> }, RunAlgorithmResponse>(
      REGISTRY,
      'RunAlgorithm',
      { algorithm, values, input },
    ),
};
