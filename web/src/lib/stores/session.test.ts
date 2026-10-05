import { afterEach, describe, expect, it } from 'vitest';
import type { Session } from '../api';
import {
  adminRole, assignmentKey, can, defaultOrg, defaultProject, isMeta, isUserKey, links, newUserUnit, ns, platformRoles, policyPrefix, rootProject, session, subjectOfKey,
  types, userKey,
} from './session.svelte';

const fixture: Session = {
  subject: 'ann',
  can: { administer: true, approve: true },
  structures: [
    { kind: 'organisation', type: 'organisation@OrgUnit', namespace: 'organisation', parent: 'organisation@part_of', root: 'ORG-DEFAULT', types: ['organisation@OrgUnit', 'organisation@User'] },
    { kind: 'project', type: 'organisation@ProjectUnit', namespace: 'organisation', parent: 'organisation@project_part_of', root: 'PROJ-ROOT', selfParent: true, default: 'default' },
  ],
  names: {
    namespaces: { organisation: 'organisation', platform: 'platform', meta: 'methodology' },
    types: { orgUnit: 'organisation@OrgUnit', projectUnit: 'organisation@ProjectUnit', user: 'organisation@User', assignment: 'organisation@Assignment', adapter: 'organisation@Adapter', policy: 'organisation@Policy', mcp: 'platform@MCP' },
    links: { partOf: 'organisation@part_of', projectPartOf: 'organisation@project_part_of', memberOf: 'organisation@member_of', assignsOrg: 'organisation@assigns_org', assignsProject: 'organisation@assigns_project' },
    keys: { user: 'USR:', assignment: 'ASG:', platformScope: 'PLATFORM', policy: 'POL:' },
    roles: { admin: 'admin' },
    props: { waiting: 'waiting' },
  },
  platformRoles: [{ name: 'admin', description: 'Administers' }, { name: 'reader' }],
};

afterEach(() => {
  session.principal = undefined;
});

describe('session accessors', () => {
  it('are empty and deny before the session is loaded', () => {
    expect(types.orgUnit).toBe('');
    expect(ns.meta).toBe('');
    expect(isMeta('methodology')).toBe(false);
    expect(can.administer).toBe(false);
    expect(can.approve).toBe(false);
    expect(platformRoles()).toEqual([]);
  });

  it('read the names the server publishes', () => {
    session.principal = fixture;
    expect(types.orgUnit).toBe('organisation@OrgUnit');
    expect(types.user).toBe('organisation@User');
    expect(links.memberOf).toBe('organisation@member_of');
    expect(ns.organisation).toBe('organisation');
    expect(policyPrefix()).toBe('POL:');
    expect(adminRole()).toBe('admin');
    expect(platformRoles().map((r) => r.name)).toEqual(['admin', 'reader']);
    expect(defaultOrg()).toBe('ORG-DEFAULT');
    expect(rootProject()).toBe('PROJ-ROOT');
    expect(can.administer).toBe(true);
  });

  it('tells the meta-domain from the others', () => {
    session.principal = fixture;
    expect(isMeta('methodology')).toBe(true);
    expect(isMeta('alm')).toBe(false);
    expect(isMeta(undefined)).toBe(false);
  });

  it('builds keys from the schemes of the server', () => {
    session.principal = fixture;
    expect(userKey('ann')).toBe('USR:ann');
    expect(isUserKey('USR:ann')).toBe(true);
    expect(isUserKey('DEP-1')).toBe(false);
    expect(subjectOfKey('USR:ann')).toBe('ann');
    expect(subjectOfKey('system:graph')).toBe('system:graph');
    expect(assignmentKey('DEP-1', 'PROJ-A')).toBe('ASG:DEP-1/PROJ-A');
    expect(assignmentKey('DEP-1')).toBe('ASG:DEP-1/PLATFORM');
    expect(assignmentKey('DEP-1', '')).toBe('ASG:DEP-1/PLATFORM');
  });

  it('picks the waiting unit and the default project, else the roots', () => {
    session.principal = fixture;
    expect(newUserUnit([{ key: 'A' }])).toBe('ORG-DEFAULT');
    expect(newUserUnit([{ key: 'B', props: { waiting: true } }, { key: 'A', props: { waiting: true } }, { key: 'C' }])).toBe('A');
    expect(defaultProject([{ key: 'P1' }])).toBe('PROJ-ROOT');
    expect(defaultProject([{ key: 'P2', props: { default: true } }, { key: 'P1' }])).toBe('P2');
  });
});
