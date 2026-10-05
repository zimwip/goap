// The session (GET /api/whoami, ADR 0070): the identity of the caller and what the server derives from it, loaded at
// sign-in, on every token change (refresh, project switch) and read through the accessors below. This is the one
// place the web learns the names of the organisation (types, links, namespaces, key schemes, roots, platform roles)
// and what the caller may attempt: none is written anywhere else (`session.test.ts` and the guard of
// `pkg/access/session_guard_test.go` keep it so).
import { whoAmI, onTokenChange, getToken, RpcError, type PlatformRole, type Session, type Structure } from '../api';
import { authState } from './auth.svelte';

export const session = $state({
  /** the answer of /api/whoami: the principal and what is derived from it (undefined: none yet, or no identity) */
  principal: undefined as Session | undefined,
  loaded: false,
  error: '',
  hasToken: !!getToken(),
});

let latest = 0;
let retry: ReturnType<typeof setTimeout> | undefined;

export async function refreshIdentity(): Promise<void> {
  const call = ++latest;
  clearTimeout(retry);
  session.hasToken = !!getToken();
  const done = (principal: Session | undefined, error: string) => {
    if (call !== latest) return; // a newer token's answer is on its way (or arrived): this one is stale
    session.principal = principal;
    session.error = error;
    session.loaded = true;
  };
  // a platform that signs users in has no identity before the sign-in; any other (a fixed dev principal) answers without a token
  if (!session.hasToken && authState.signsIn) return done(undefined, '');
  try {
    done(await whoAmI(), '');
  } catch (e) {
    done(undefined, e instanceof Error ? e.message : 'unknown identity');
    // the platform cannot read its organisation yet (503, e.g. while it starts): ask again soon
    if (e instanceof RpcError && e.status === 503) retry = setTimeout(() => void refreshIdentity(), 3000);
  }
}

onTokenChange(() => void refreshIdentity());

/** Current subject ('': anonymous or unknown). */
export function me(): string {
  return session.principal?.subject ?? '';
}

// --- what the server says of the organisation -------------------------------------------------------------------

const none = '';

/** Namespaces of the built-in domains and the meta-domain. */
export const ns = {
  get organisation() {
    return session.principal?.names.namespaces.organisation ?? none;
  },
  get platform() {
    return session.principal?.names.namespaces.platform ?? none;
  },
  get meta() {
    return session.principal?.names.namespaces.meta ?? none;
  },
};

/** Whether a namespace is the meta-domain (the definition nodes of the methodologies), left out of the views of what is worked on. */
export function isMeta(namespace: string | undefined): boolean {
  return !!namespace && namespace === ns.meta;
}

/** Qualified node types. The units and the projects are those of the structures. */
export const types = {
  get orgUnit() {
    return session.principal?.names.types.orgUnit ?? none;
  },
  get projectUnit() {
    return session.principal?.names.types.projectUnit ?? none;
  },
  get user() {
    return session.principal?.names.types.user ?? none;
  },
  get assignment() {
    return session.principal?.names.types.assignment ?? none;
  },
  get adapter() {
    return session.principal?.names.types.adapter ?? none;
  },
  get policy() {
    return session.principal?.names.types.policy ?? none;
  },
  get mcp() {
    return session.principal?.names.types.mcp ?? none;
  },
};

/** Qualified link types. */
export const links = {
  get partOf() {
    return session.principal?.names.links.partOf ?? none;
  },
  get projectPartOf() {
    return session.principal?.names.links.projectPartOf ?? none;
  },
  get memberOf() {
    return session.principal?.names.links.memberOf ?? none;
  },
  get assignsOrg() {
    return session.principal?.names.links.assignsOrg ?? none;
  },
  get assignsProject() {
    return session.principal?.names.links.assignsProject ?? none;
  },
};

/** Name of the OrgUnit property flagging the waiting unit new users join (ADR 0042). */
export const waitingProp = () => session.principal?.names.props.waiting ?? none;

const structureOf = (type: string): Structure | undefined => session.principal?.structures.find((s) => s.type === type);

/** Key of the root unit: the unit that holds what names none, and the default of the units. */
export const defaultOrg = (): string => structureOf(types.orgUnit)?.root ?? none;

/** Key of the root project. */
export const rootProject = (): string => structureOf(types.projectUnit)?.root ?? none;

type Flagged = { key?: string; props?: Record<string, unknown> };

/** The first of the nodes carrying the flag (smallest key), else the fallback. */
function flagged(nodes: Flagged[], prop: string, fallback: string): string {
  if (!prop) return fallback;
  return (
    nodes
      .filter((n) => n.props?.[prop] === true)
      .map((n) => n.key ?? '')
      .sort()[0] || fallback
  );
}

/** The key of the unit new users join: the waiting unit (smallest key if several carry the flag), else the root unit. */
export const newUserUnit = (units: Flagged[]): string => flagged(units, waitingProp(), defaultOrg());

/** The property flagging the default project ('' when the root is the default). */
export const defaultProjectProp = (): string => structureOf(types.projectUnit)?.default ?? none;

/** The key of the default project: the flagged project (smallest key if several carry the flag), else the root project. */
export const defaultProject = (projects: Flagged[]): string => flagged(projects, defaultProjectProp(), rootProject());

// --- keys -------------------------------------------------------------------------------------------------------

const keys = () => session.principal?.names.keys;

/** The key of the node of a user (also the key of their personal unit). */
export const userKey = (subject: string): string => `${keys()?.user ?? none}${subject}`;

/** Whether a key is that of a user node. */
export const isUserKey = (key: string | undefined): boolean => !!keys()?.user && !!key?.startsWith(keys()!.user);

/** The subject a user key stands for (the key itself when it is not one). */
export const subjectOfKey = (key: string): string => (isUserKey(key) ? key.slice(keys()!.user.length) : key);

/** The key of the Assignment granting a unit or user roles on a project, or platform-wide when no project is named. */
export const assignmentKey = (org: string, project?: string): string =>
  `${keys()?.assignment ?? none}${org}/${project || (keys()?.platformScope ?? none)}`;

/** The prefix of the key of a Policy node (the caller adds its own suffix). */
export const policyPrefix = (): string => keys()?.policy ?? none;

// --- roles and capabilities -------------------------------------------------------------------------------------

/** Name of the platform role of an administrator (what the platform Assignment of a user holds to make them one). */
export const adminRole = (): string => session.principal?.names.roles.admin ?? none;

/** The built-in platform roles an Assignment grants platform-wide. */
export const platformRoles = (): PlatformRole[] => session.principal?.platformRoles ?? [];

/**
 * What the caller may attempt, to show or hide controls: hints, the server enforces every call.
 * administer: the controls only platform administrators use (organisation, projects, policies, adapters, the model
 * catalogue and quotas, the usage of the whole platform, the index). approve: they work on their active project, so
 * an approval of someone else's run may be theirs to give (the engine decides run by run).
 */
export const can = {
  get administer() {
    return !!session.principal?.can.administer;
  },
  get approve() {
    return !!session.principal?.can.approve;
  },
};
