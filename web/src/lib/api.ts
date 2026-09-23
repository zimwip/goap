// Client minimal pour la passerelle GOAP (protocole Connect, encodage JSON).
// Chaque RPC est un `POST /{package.Service}/{Method}` avec un corps JSON
// (proto3 JSON : champs en lowerCamelCase, valeurs par défaut omises).

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

const TOKEN_KEY = 'goap.token';

/** Base des URL RPC : relative par défaut (le serveur Vite relaie `/goap.*`). */
export const BASE = (import.meta.env.VITE_GOAP_BASE_URL as string | undefined) ?? '';

/** Base de l'interface Jaeger pour les liens « Trace ». */
export const JAEGER_URL = ((import.meta.env.VITE_GOAP_JAEGER_URL as string | undefined) ?? 'http://localhost:16686').replace(
  /\/+$/,
  '',
);

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

const tokenListeners = new Set<() => void>();

/** Abonnement aux changements de jeton (relance des flux). Renvoie la fonction de désabonnement. */
export function onTokenChange(fn: () => void): () => void {
  tokenListeners.add(fn);
  return () => tokenListeners.delete(fn);
}

export function setToken(token: string | null): void {
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    // stockage indisponible (navigation privée, etc.) : on ignore
  }
  for (const fn of tokenListeners) fn();
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
  if (e instanceof RpcError) {
    if (e.code === 'permission_denied')
      return `Accès refusé : vous n'avez pas les droits nécessaires pour cette opération${e.message ? ` (${e.message})` : ''}.`;
    if (e.code === 'unauthenticated')
      return `Authentification requise : configurez un jeton d'accès valide${e.message ? ` (${e.message})` : ''}.`;
    return e.code ? `${e.code} : ${e.message}` : e.message;
  }
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

/** draft : modifiable · published : figée (seule exécutable) · archived : lecture seule */
export type MethodologyStatus = 'draft' | 'published' | 'archived';

export interface NodeType {
  name?: string;
  description?: string;
  properties?: string[];
}

export interface LinkType {
  name?: string;
  from?: string;
  to?: string;
}

export interface Condition {
  name?: string;
  description?: string;
  /** Expression CEL évaluée sur le tableau noir. */
  expr?: string;
}

export interface ProduceSpec {
  op?: 'create_node' | 'update_node' | string;
  nodeType?: string;
}

export interface LinkSpec {
  type?: string;
  direction?: 'out' | 'in' | string;
}

export interface Expectation {
  forEach?: 'impacts' | 'proposals' | 'items' | 'artifacts' | string;
  where?: string;
  produce?: ProduceSpec;
  link?: LinkSpec;
}

export type ActionKind = 'llm' | 'script' | 'tool' | 'human' | 'builtin';
export type ScriptLanguage = 'javascript' | 'go';
export type PlannerKind = 'goap' | 'utility' | 'hybrid';

export interface Action {
  name?: string;
  description?: string;
  kind?: ActionKind | string;
  pre?: Record<string, boolean>;
  effects?: Record<string, boolean>;
  cost?: number;
  expects?: Expectation;
  /** « <ressource>:<action> » exigée de l'initiateur, ex. change:apply */
  permission?: string;
  model?: string;
  prompt?: string;
  tool?: string;
  builtin?: string;
  instructions?: string;
  params?: Struct;
  /** actions script : javascript | go */
  language?: ScriptLanguage | string;
  /** code exécuté dans le sandbox avec le DSL `ctx` */
  code?: string;
  /** expression CEL numérique (planificateurs utility / hybrid) */
  utility?: string;
}

export type TriggerType = 'event' | 'schedule';
export const TRIGGER_EVENTS = [
  'change.created',
  'change.applied',
  'change.item_added',
  'process.completed',
  'process.failed',
  'methodology.published',
] as const;

/** Déclencheur : exécution automatique d'un agent (hors boucle d'intention). */
export interface Trigger {
  name?: string;
  description?: string;
  type?: TriggerType | string;
  /** déclencheurs « event » */
  event?: string;
  /** filtre CEL sur l'événement */
  filter?: string;
  /** déclencheurs « schedule » : cron à 5 champs, UTC */
  schedule?: string;
  goal?: string;
  intent?: string;
  /** new_change (défaut) | event_change */
  target?: 'new_change' | 'event_change' | string;
  roles?: string[];
  enabled?: boolean;
}

/** Agent (terminologie Embabel) : un planificateur et ses actions admissibles. */
export interface Agent {
  name?: string;
  description?: string;
  examples?: string[];
  planner?: PlannerKind | string;
  /** noms des actions admissibles (vide : toutes) */
  actions?: string[];
  /** noms des objectifs (vide : tous) */
  goals?: string[];
  triggers?: Trigger[];
}

export interface Goal {
  name?: string;
  description?: string;
  examples?: string[];
  pre?: Record<string, boolean>;
  value?: number;
}

export interface Methodology {
  name?: string;
  version?: string;
  description?: string;
  status?: MethodologyStatus | string;
  nodeTypes?: NodeType[];
  linkTypes?: LinkType[];
  conditions?: Condition[];
  actions?: Action[];
  goals?: Goal[];
  agents?: Agent[];
  createdAt?: string;
  updatedAt?: string;
  publishedAt?: string;
  updatedBy?: string;
}

export interface GoalSummary {
  name?: string;
  description?: string;
}

export interface AgentSummary {
  name?: string;
  description?: string;
  planner?: string;
}

export interface MethodologySummary {
  name?: string;
  version?: string;
  description?: string;
  status?: MethodologyStatus | string;
  goals?: GoalSummary[];
  agents?: AgentSummary[];
  updatedAt?: string;
  publishedAt?: string;
}

/** Problème de validation ; `path` localise le champ, ex. « conditions[2].expr ». */
export interface Issue {
  path?: string;
  message?: string;
}

// --- iam --------------------------------------------------------------------

/** Règle ABAC : `rule` est une expression sur r.sub, r.obj et r.act. */
export interface Policy {
  rule?: string;
  /** type de ressource ou « * » */
  resource?: string;
  /** action ou « * » */
  action?: string;
  effect?: 'allow' | 'deny' | string;
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
  agent?: string;
  methodology?: string;
}

/** Entier 64 bits : proto3 JSON le sérialise en chaîne. */
export type Int64 = number | string;

export interface Usage {
  inputTokens?: Int64;
  outputTokens?: Int64;
  llmCalls?: number;
  toolCalls?: number;
}

export interface LlmCall {
  provider?: string;
  model?: string;
  inputTokens?: Int64;
  outputTokens?: Int64;
  durationMs?: Int64;
  error?: string;
}

export interface ToolCall {
  name?: string;
  durationMs?: Int64;
  error?: string;
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface LogLine {
  time?: string;
  level?: LogLevel | string;
  message?: string;
  processId?: string;
  action?: string;
  step?: number;
}

export interface Principal {
  subject?: string;
  org?: string;
  roles?: string[];
}
export interface HumanTask {
  /** input : saisir des items · approval : approuver ou refuser l'action · agent : attente d'un sous-agent */
  kind?: 'input' | 'approval' | 'agent' | string;
  /** kind « agent » : processus du sous-agent attendu */
  childProcessId?: string;
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
  usage?: Usage;
  llmCalls?: LlmCall[];
  toolCalls?: ToolCall[];
  logs?: LogLine[];
  /** processus des sous-agents lancés par l'étape */
  childProcessIds?: string[];
  /** sandbox ayant exécuté l'étape (actions script) */
  sandbox?: string;
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
  agent?: string;
  planner?: PlannerKind | string;
  /** processus appelant (sous-agent) */
  parentId?: string;
  usage?: Usage;
  baselineId?: string;
  title?: string;
  /** trace OpenTelemetry (span racine « process ») */
  traceId?: string;
  /** « <agent>/<déclencheur> » quand lancé par un déclencheur */
  trigger?: string;
}

/** État d'un déclencheur d'un agent publié. */
export interface TriggerState {
  methodology?: string;
  agent?: string;
  name?: string;
  description?: string;
  type?: TriggerType | string;
  event?: string;
  schedule?: string;
  enabled?: boolean;
  fires?: number;
  lastFired?: string;
  nextFire?: string;
  lastProcessId?: string;
  lastError?: string;
}

export interface ListProcessesRequest {
  /** seulement les processus lancés par l'appelant */
  mine?: boolean;
  statuses?: string[];
  /** seulement les processus racines (pas de sous-agents) */
  rootsOnly?: boolean;
}

export type EventType = 'started' | 'intent' | 'step' | 'waiting' | 'completed' | 'stuck' | 'failed' | 'log';

/** Message du flux WatchEvents. */
export interface WatchEvent {
  type?: EventType | string;
  time?: string;
  process?: Process;
  log?: LogLine;
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

const IAM = 'goap.iam.v1.IamService';

export const ENGINE_SERVICE = ENGINE;

type NameVersion = { name: string; version: string };

export const registry = {
  /** `allVersions` : toutes les versions (brouillons, archivées) au lieu de la dernière par nom. */
  listMethodologies: (allVersions = false, signal?: AbortSignal) =>
    rpc<{ allVersions?: boolean }, { methodologies?: MethodologySummary[] }>(
      REGISTRY,
      'ListMethodologies',
      allVersions ? { allVersions } : {},
      signal,
    ),
  /** `version` vide : dernière version publiée. */
  getMethodology: (name: string, version = '', signal?: AbortSignal) =>
    rpc<NameVersion, { methodology?: Methodology }>(REGISTRY, 'GetMethodology', { name, version }, signal),
  saveMethodology: (methodology: Methodology) =>
    rpc<{ methodology: Methodology }, { methodology?: Methodology; issues?: Issue[] }>(REGISTRY, 'SaveMethodology', {
      methodology,
    }),
  validateMethodology: (methodology: Methodology) =>
    rpc<{ methodology: Methodology }, { issues?: Issue[] }>(REGISTRY, 'ValidateMethodology', { methodology }),
  publishMethodology: (name: string, version: string) =>
    rpc<NameVersion, { methodology?: Methodology }>(REGISTRY, 'PublishMethodology', { name, version }),
  createVersion: (name: string, fromVersion: string, newVersion: string) =>
    rpc<{ name: string; fromVersion: string; newVersion: string }, { methodology?: Methodology }>(
      REGISTRY,
      'CreateVersion',
      { name, fromVersion, newVersion },
    ),
  /** Supprime un brouillon, ou archive une version publiée. */
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
};

export const iam = {
  whoAmI: (signal?: AbortSignal) => rpc<Empty, { principal?: Principal }>(IAM, 'WhoAmI', {}, signal),
  listPolicies: (signal?: AbortSignal) =>
    rpc<Empty, { policies?: Policy[] }>(IAM, 'ListPolicies', {}, signal),
  addPolicy: (policy: Policy) => rpc<{ policy: Policy }, { policy?: Policy }>(IAM, 'AddPolicy', { policy }),
  removePolicy: (policy: Policy) => rpc<{ policy: Policy }, Empty>(IAM, 'RemovePolicy', { policy }),
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
  /** vide : identification parmi toutes les méthodologies publiées et leurs agents */
  methodology?: string;
  /** restreint l'identification à cet agent */
  agent?: string;
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
  listProcesses: (req: ListProcessesRequest = {}, signal?: AbortSignal) =>
    rpc<ListProcessesRequest, { processes?: Process[] }>(ENGINE, 'ListProcesses', req, signal),
  listTriggers: (signal?: AbortSignal) =>
    rpc<Empty, { triggers?: TriggerState[] }>(ENGINE, 'ListTriggers', {}, signal),
  fireTrigger: (methodology: string, agent: string, trigger: string) =>
    rpc<{ methodology: string; agent: string; trigger: string }, { process?: Process }>(ENGINE, 'FireTrigger', {
      methodology,
      agent,
      trigger,
    }),
};

// --- état de la plateforme (passerelle, hors RPC) -------------------------------

export interface ServiceStatus {
  name?: string;
  status?: 'up' | 'down' | string;
  latencyMs?: number;
  error?: string;
}

export interface PlatformStatus {
  status?: 'ok' | 'degraded' | 'down' | string;
  services?: ServiceStatus[];
  time?: string;
}

/** `GET /api/status` servi par la passerelle. */
export async function platformStatus(signal?: AbortSignal): Promise<PlatformStatus> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const token = getToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  let res: Response;
  try {
    res = await fetch(`${BASE}/api/status`, { headers, signal });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new RpcError('unavailable', `Passerelle injoignable : ${String(e)}`, 0);
  }
  const text = await res.text();
  let data: PlatformStatus | undefined;
  try {
    data = text ? (JSON.parse(text) as PlatformStatus) : undefined;
  } catch {
    data = undefined;
  }
  // 503 avec un corps d'état : plateforme indisponible, mais réponse exploitable.
  if (data?.status) return data;
  throw new RpcError(res.status === 404 ? 'unimplemented' : 'unknown', text || res.statusText, res.status);
}

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

/** Compare deux numéros de version « 1.2.10 » segment par segment (numérique si possible). */
export function compareVersions(a: string | undefined, b: string | undefined): number {
  const pa = (a ?? '').split(/[.-]/);
  const pb = (b ?? '').split(/[.-]/);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? '';
    const y = pb[i] ?? '';
    const nx = Number(x);
    const ny = Number(y);
    const c = x !== '' && y !== '' && !Number.isNaN(nx) && !Number.isNaN(ny) ? nx - ny : x.localeCompare(y);
    if (c !== 0) return c;
  }
  return 0;
}

/** Incrémente le dernier segment numérique : 1.2.3 → 1.2.4. */
export function bumpPatch(version: string | undefined): string {
  const v = version ?? '';
  const m = /^(.*?)(\d+)(\D*)$/.exec(v);
  if (!m) return v ? `${v}.1` : '0.1.0';
  return `${m[1]}${Number(m[2]) + 1}${m[3]}`;
}

/** Valeur numérique d'un entier proto3 (nombre ou chaîne pour les int64). */
export function int(v: Int64 | undefined | null): number {
  if (v === undefined || v === null || v === '') return 0;
  const n = typeof v === 'number' ? v : Number(v);
  return Number.isFinite(n) ? n : 0;
}

/** 12345 → « 12 345 ». */
export function formatInt(v: Int64 | undefined | null): string {
  return int(v).toLocaleString('fr-FR');
}

export function formatDuration(ms: Int64 | undefined | null): string {
  const n = int(ms);
  if (n < 1000) return `${n} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(1)} s`;
  return `${Math.floor(n / 60_000)} min ${Math.round((n % 60_000) / 1000)} s`;
}

export function formatTime(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString('fr-FR', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : '';
}
