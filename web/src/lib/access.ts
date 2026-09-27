// Who may do what is graph data of the organisation namespace: Policy nodes (ABAC rules) and User nodes
// (profile and roles), changed through changes like any node.
import type { GraphNode, Policy, Struct } from './api';

export const NS_ORGANISATION = 'organisation';
export const POLICY_TYPE = 'organisation@Policy';
export const USER_TYPE = 'organisation@User';
export const MEMBER_OF = 'organisation@member_of';

export interface User {
  subject: string;
  displayName: string;
  email: string;
  locale: string;
  roles: string[];
}

export const userKey = (subject: string) => `USR:${subject}`;
export const newPolicyKey = (p: Policy) => `POL:${p.resource}/${p.action}/${Math.random().toString(16).slice(2, 10)}`;

export function policiesOf(nodes: GraphNode[]): { node: GraphNode; policy: Policy }[] {
  return nodes
    .filter((n) => n.namespace === NS_ORGANISATION && n.type === POLICY_TYPE)
    .map((node) => {
      const p = (node.props ?? {}) as Record<string, unknown>;
      return { node, policy: { rule: String(p.rule ?? ''), resource: String(p.resource ?? ''), action: String(p.action ?? ''), effect: String(p.effect ?? '') } };
    })
    .sort((a, b) => `${a.policy.resource}/${a.policy.action}`.localeCompare(`${b.policy.resource}/${b.policy.action}`));
}

export const policyProps = (p: Policy): Struct => ({ rule: p.rule ?? '', resource: p.resource ?? '', action: p.action ?? '', effect: p.effect ?? '' });

export function usersOf(nodes: GraphNode[]): { node: GraphNode; user: User }[] {
  return nodes
    .filter((n) => n.namespace === NS_ORGANISATION && n.type === USER_TYPE)
    .map((node) => {
      const p = (node.props ?? {}) as Record<string, unknown>;
      return {
        node,
        user: {
          subject: String(p.subject ?? ''),
          displayName: String(p.displayName ?? ''),
          email: String(p.email ?? ''),
          locale: String(p.locale ?? ''),
          roles: Array.isArray(p.roles) ? (p.roles as unknown[]).map(String) : [],
        },
      };
    })
    .sort((a, b) => a.user.subject.localeCompare(b.user.subject));
}

/** properties of the node (empty optional fields are left out) */
export function userProps(u: User): Struct {
  return {
    subject: u.subject,
    ...(u.displayName ? { displayName: u.displayName } : {}),
    ...(u.email ? { email: u.email } : {}),
    ...(u.locale ? { locale: u.locale } : {}),
    ...(u.roles.length ? { roles: u.roles } : {}),
  };
}
