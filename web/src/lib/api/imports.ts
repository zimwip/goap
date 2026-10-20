import { rpc } from './transport';
import type { JsonValue } from './types/common';
import type { BrowseImportResponseJson, ImporterJson } from '../../gen/goap/importer/v1/importer_pb';
import type { Job } from './jobs';

const IMPORTS = 'goap.importer.v1.ImportService';

/** An importer a domain declares (ADR 0148), with whether a live service implements it. */
export type Importer = ImporterJson;
/** The directories a parameter of an importer may name, as its service reads them (the folder chooser). */
export type BrowseListing = BrowseImportResponseJson;
/** A run of an importer: a job of kind import.<domain>.<importer> (ADR 0149). */
export type ImportJob = Job;

/**
 * The import registry (ADR 0148), served by the graph: the sources the domains declare, available while an importer
 * service is a live worker of the job service. A run is a job (ADR 0149), followed and cancelled through the job
 * service; it writes through the change operations, one change per unit of the source (a commit for git).
 */
export const imports = {
  /** The importers the domains in force declare; with `namespace`, those of its domain. */
  list: (namespace = '', signal?: AbortSignal) =>
    rpc<{ namespace: string }, { importers?: Importer[] }>(IMPORTS, 'ListImporters', { namespace }, signal),
  /** Runs an importer for the caller in their active project (or `projectId`): the job, queued. */
  start: (req: { domain: string; importer: string; params: Record<string, JsonValue>; projectId?: string; ownerOrg?: string }) =>
    rpc<object, { job: ImportJob }>(IMPORTS, 'StartImport', req),
  /** The directories under `path` (empty: the roots the service reads) a parameter whose widget is `directory` may name. */
  browse: (req: { domain: string; importer: string; param: string; path?: string; projectId?: string }, signal?: AbortSignal) =>
    rpc<object, BrowseListing>(IMPORTS, 'BrowseImport', req, signal),
};
