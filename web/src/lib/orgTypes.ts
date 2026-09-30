// Qualified types and links of the organisation and platform domains the IDE writes and reads (ADR 0012):
// the built-in organisation and platform domains (domains/builtin/).
export const ORG_UNIT_TYPE = 'organisation@OrgUnit';
export const ADAPTER_TYPE = 'organisation@Adapter';
export const PART_OF = 'organisation@part_of';
export const MEMBER_OF = 'organisation@member_of';
export const OWNER = 'organisation@owner';
export const MCP_TYPE = 'platform@MCP';

// Users, projects and assignments (ADR 0039, 0020): User extends OrgUnit (the smallest organisational
// unit is a person), Project mirrors OrgUnit's hierarchy, and Assignment is the meeting point of the two,
// granting an org unit or user the roles it locally holds on a project.
export const USER_TYPE = 'organisation@User';
export const PROJECT_UNIT_TYPE = 'organisation@ProjectUnit';
export const ASSIGNMENT_TYPE = 'organisation@Assignment';
export const PROJECT_PART_OF = 'organisation@project_part_of';
export const ASSIGNS_ORG = 'organisation@assigns_org';
export const ASSIGNS_PROJECT = 'organisation@assigns_project';

// Mirrors domain.DefaultOrg / domain.DefaultProject (pkg/domain/domain.go): the two seeded roots, the only
// units that need no parent (ADR 0040).
export const DEFAULT_ORG = 'ORG-DEFAULT';
export const DEFAULT_PROJECT = 'PROJ-ROOT';

// The OrgUnit property flagging the waiting unit (ADR 0042; mirrors access.PropWaitingUnit): a unit an
// administrator creates at their discretion for users signing in for the first time, linked member_of it
// until an administrator moves them; with none flagged, new users join ORG-DEFAULT.
export const WAITING_UNIT_PROP = 'waiting';

/** The key of the unit new users join: the waiting unit (smallest key if several carry the flag), else ORG-DEFAULT. */
export function newUserUnit(units: { key?: string; props?: Record<string, unknown> }[]): string {
  return (
    units
      .filter((n) => n.props?.[WAITING_UNIT_PROP] === true)
      .map((n) => n.key ?? '')
      .sort()[0] || DEFAULT_ORG
  );
}
