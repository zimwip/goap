// Qualified types and links of the organisation and platform domains the IDE writes and reads (ADR 0012):
// the built-in organisation and platform domains (domains/builtin/).
export const ORG_UNIT_TYPE = 'organisation@OrgUnit';
export const ADAPTER_TYPE = 'organisation@Adapter';
export const PART_OF = 'organisation@part_of';
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
