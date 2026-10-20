import { rpc } from './transport';
import type { JsonValue } from './types/common';
import type { ImporterJson, JobJson } from '../../gen/goap/importer/v1/importer_pb';

const IMPORTS = 'goap.importer.v1.ImportService';

/** An importer a domain declares (ADR 0148), with whether a live service implements it. */
export type Importer = ImporterJson;
/** A run of an importer. */
export type ImportJob = JobJson;

/**
 * The import registry (ADR 0148), served by the graph: the sources the domains declare, the runs the importer services
 * hold. A run writes through the change operations, one change per unit of the source (a commit for git).
 */
export const imports = {
  /** The importers the domains in force declare; with `namespace`, those of its domain. */
  list: (namespace = '', signal?: AbortSignal) =>
    rpc<{ namespace: string }, { importers?: Importer[] }>(IMPORTS, 'ListImporters', { namespace }, signal),
  /** Runs an importer for the caller in their active project (or `projectId`). */
  start: (req: { domain: string; importer: string; params: Record<string, JsonValue>; projectId?: string; ownerOrg?: string }) =>
    rpc<object, { job: ImportJob }>(IMPORTS, 'StartImport', req),
  /** The runs (every one for an administrator, the caller's own otherwise), newest first. */
  jobs: (namespace = '', signal?: AbortSignal) =>
    rpc<{ namespace: string }, { jobs?: ImportJob[] }>(IMPORTS, 'ListImportJobs', { namespace }, signal),
  cancel: (service: string, id: string) => rpc<{ service: string; id: string }, { job: ImportJob }>(IMPORTS, 'CancelImportJob', { service, id }),
};
