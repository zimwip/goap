// Client minimal pour la passerelle GOAP (protocole Connect, encodage JSON).
// Chaque RPC est un `POST /{package.Service}/{Method}` avec un corps JSON
// (proto3 JSON : champs en lowerCamelCase, valeurs par défaut omises).

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

const TOKEN_KEY = 'goap.token';

/** Base des URL RPC : relative par défaut (le serveur Vite relaie `/goap.*`). */
const BASE = (import.meta.env.VITE_GOAP_BASE_URL as string | undefined) ?? '';

export class RpcError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = 'RpcError';
    this.code = code;
    this.status = status;
  }
}

export function getToken(): string | null {
  try {
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

export function setToken(token: string | null): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // stockage indisponible (navigation privée, etc.) : on ignore
  }
}

export async function rpc<TReq extends object, TRes>(
  service: string,
  method: string,
  body: TReq,
  signal?: AbortSignal,
): Promise<TRes> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;

  let res: Response;
  try {
    res = await fetch(`${BASE}/${service}/${method}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
      signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new RpcError('unavailable', `Passerelle injoignable : ${String(e)}`, 0);
  }

  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }

  if (!res.ok) {
    const err = (data ?? {}) as { code?: string; message?: string };
    throw new RpcError(err.code ?? 'unknown', err.message ?? (text || res.statusText), res.status);
  }
  return (data ?? {}) as TRes;
}

/** Message d'erreur lisible pour l'interface. */
export function errorMessage(e: unknown): string {
  if (e instanceof RpcError) return e.code ? `${e.code} : ${e.message}` : e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}

// ---------------------------------------------------------------------------
// Types (proto3 JSON). Les champs sont optionnels car les valeurs par défaut
// sont omises à la sérialisation.
// ---------------------------------------------------------------------------

export type JsonValue = null | boolean | number | string | JsonValue[] | { [k: string]: JsonValue };
export type Struct = { [k: string]: JsonValue };
type Empty = Record<string, never>;

// --- registry ---------------------------------------------------------------

export interface GoalSummary {
  name?: string;
  description?: string;
}

export interface Methodology {
  name?: string;
  version?: string;
  description?: string;
  source?: string;
  goals?: GoalSummary[];
  publishedAt?: string;
}

// --- graph ------------------------------------------------------------------

export interface NodeRef {
  id?: string;
  version?: number;
}

export interface GraphNode {
  id?: string;
  version?: number;
  key?: string;
  type?: string;
  props?: Struct;
  deleted?: boolean;
  changeId?: string;
  createdAt?: string;
}

export interface Link {
  id?: string;
  type?: string;
  from?: NodeRef;
  to?: NodeRef;
  props?: Struct;
  changeId?: string;
}

export interface Baseline {
  id?: string;
  name?: string;
  parentId?: string;
  changeId?: string;
  nodes?: Record<string, number>;
  createdAt?: string;
}

export interface Endpoint {
  node?: NodeRef;
  item?: string;
}

export interface NodeDraft {
  base?: NodeRef;
  key?: string;
  type?: string;
  props?: Struct;
}

export interface LinkDraft {
  linkId?: string;
  type?: string;
  from?: Endpoint;
  to?: Endpoint;
  props?: Struct;
}

export interface Proposal {
  op?: 'create_node' | 'update_node' | 'delete_node' | 'add_link' | 'remove_link' | string;
  node?: NodeDraft;
  link?: LinkDraft;
}

export interface Decision {
  item?: string;
  accept?: boolean;
  comment?: string;
}

export type ItemKind = 'impact' | 'proposal' | 'decision' | 'artifact';

export interface ChangeItem {
  id?: string;
  kind?: ItemKind | string;
  type?: string;
  status?: string;
  target?: NodeRef;
  proposal?: Proposal;
  decision?: Decision;
  data?: Struct;
  producedBy?: string;
  derivedFrom?: string[];
  createdAt?: string;
}

export interface ChangeSet {
  id?: string;
  title?: string;
  intent?: string;
  methodology?: string;
  goal?: string;
  status?: 'draft' | 'active' | 'applied' | 'abandoned' | string;
  baselineId?: string;
  resultBaselineId?: string;
  data?: Struct;
  items?: ChangeItem[];
  createdAt?: string;
}

// --- engine -----------------------------------------------------------------

export type ProcessStatus = 'clarifying' | 'running' | 'waiting' | 'completed' | 'stuck' | 'failed';

export interface Turn {
  role?: string;
  text?: string;
}

export interface Candidate {
  goal?: string;
  confidence?: number;
  reason?: string;
}

export interface Principal {
  subject?: string;
  org?: string;
  roles?: string[];
}
export interface HumanTask {
  /** input : saisir des items · approval : approuver ou refuser l'action */
  kind?: 'input' | 'approval' | string;
  /** permission requise pour approuver (ex. change:apply) */
  permission?: string;
  action?: string;
  description?: string;
  instructions?: string;
  step?: number;
}

export interface Step {
  index?: number;
  action?: string;
  plan?: string[];
  before?: Record<string, boolean>;
  after?: Record<string, boolean>;
  items?: string[];
  effectsMet?: boolean;
  approvedBy?: string;
  output?: string;
  error?: string;
  startedAt?: string;
  endedAt?: string;
}

export interface Process {
  id?: string;
  methodology?: string;
  changeId?: string;
  status?: ProcessStatus | string;
  goal?: string;
  turns?: Turn[];
  question?: string;
  candidates?: Candidate[];
  pending?: HumanTask;
  plan?: string[];
  world?: Record<string, boolean>;
  unknown?: Record<string, string>;
  steps?: Step[];
  disabled?: string[];
  error?: string;
  createdAt?: string;
  updatedAt?: string;
  initiator?: Principal;
}

/** Item au format d'entrée du moteur (pkg/engine.ItemInput). */
export interface ItemInput {
  ref?: string;
  kind: ItemKind;
  type?: string;
  /** Clé du nœud ciblé. */
  target?: string;
  proposal?: Struct;
  decision?: { item: string; accept: boolean; comment?: string };
  data?: Struct;
  derivedFrom?: string[];
}

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

const REGISTRY = 'goap.registry.v1.RegistryService';
const GRAPH = 'goap.graph.v1.GraphService';
const ENGINE = 'goap.engine.v1.EngineService';

export const registry = {
  listMethodologies: (signal?: AbortSignal) =>
    rpc<Empty, { methodologies?: Methodology[] }>(REGISTRY, 'ListMethodologies', {}, signal),
};

export const graph = {
  listBaselines: (signal?: AbortSignal) =>
    rpc<Empty, { baselines?: Baseline[] }>(GRAPH, 'ListBaselines', {}, signal),
  getBaselineGraph: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { baseline?: Baseline; nodes?: GraphNode[]; links?: Link[]; suspectLinks?: Link[] }>(
      GRAPH,
      'GetBaselineGraph',
      { id },
      signal,
    ),
  listChanges: (signal?: AbortSignal) =>
    rpc<Empty, { changes?: ChangeSet[] }>(GRAPH, 'ListChanges', {}, signal),
  getChange: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { change?: ChangeSet }>(GRAPH, 'GetChange', { id }, signal),
  applyChange: (changeId: string, baselineName: string) =>
    rpc<{ changeId: string; baselineName: string }, { baseline?: Baseline }>(GRAPH, 'ApplyChange', {
      changeId,
      baselineName,
    }),
};

export interface StartProcessRequest {
  methodology: string;
  baselineId?: string;
  changeId?: string;
  title?: string;
  intent: string;
  goal?: string;
  vars?: Struct;
}

export const engine = {
  startProcess: (req: StartProcessRequest) =>
    rpc<StartProcessRequest, { process?: Process }>(ENGINE, 'StartProcess', req),
  answerIntent: (processId: string, answer: string) =>
    rpc<{ processId: string; answer: string }, { process?: Process }>(ENGINE, 'AnswerIntent', {
      processId,
      answer,
    }),
  submitHumanInput: (processId: string, items: ItemInput[]) =>
    rpc<{ processId: string; items: ItemInput[] }, { process?: Process }>(ENGINE, 'SubmitHumanInput', {
      processId,
      items,
    }),
  approveAction: (processId: string, approve: boolean, comment: string) =>
    rpc<{ processId: string; approve: boolean; comment: string }, { process?: Process }>(ENGINE, 'ApproveAction', {
      processId,
      approve,
      comment,
    }),
  getProcess: (id: string, signal?: AbortSignal) =>
    rpc<{ id: string }, { process?: Process }>(ENGINE, 'GetProcess', { id }, signal),
  listProcesses: (signal?: AbortSignal) =>
    rpc<Empty, { processes?: Process[] }>(ENGINE, 'ListProcesses', {}, signal),
};

// ---------------------------------------------------------------------------
// Utilitaires d'affichage
// ---------------------------------------------------------------------------

/** Titre lisible d'un nœud (propriété `title`, sinon `name`). */
export function nodeTitle(n: GraphNode | undefined): string {
  const t = n?.props?.['title'] ?? n?.props?.['name'];
  return typeof t === 'string' ? t : '';
}

export function formatDate(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString('fr-FR', { dateStyle: 'short', timeStyle: 'medium' });
}

export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : '';
}
