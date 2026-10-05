// Who may do what is graph data of the organisation namespace: Policy nodes (ABAC rules) and User nodes
// (profile; their roles, administration included, are granted through Assignments, ADR 0043, 0047), changed
// through changes like any node.
import type { GraphNode, Policy, Struct } from './api';
import { ns, policyPrefix, types } from './stores/session.svelte';

export interface User {
  subject: string;
  displayName: string;
  email: string;
  locale: string;
}

// A policy created in the editor is keyed by its target and a random suffix: the key only identifies the node (the
// server's own digest, access.PolicyKey, names the policies it seeds, and nothing derives a key from a rule).
export const newPolicyKey = (p: Policy) => `${policyPrefix()}${p.resource}/${p.action}/${Math.random().toString(16).slice(2, 10)}`;

export function policiesOf(nodes: GraphNode[]): { node: GraphNode; policy: Policy }[] {
  return nodes
    .filter((n) => n.namespace === ns.organisation && n.type === types.policy)
    .map((node) => {
      const p = (node.props ?? {}) as Record<string, unknown>;
      return { node, policy: { rule: String(p.rule ?? ''), resource: String(p.resource ?? ''), action: String(p.action ?? ''), effect: String(p.effect ?? '') } };
    })
    .sort((a, b) => `${a.policy.resource}/${a.policy.action}`.localeCompare(`${b.policy.resource}/${b.policy.action}`));
}

export const policyProps = (p: Policy): Struct => ({ rule: p.rule ?? '', resource: p.resource ?? '', action: p.action ?? '', effect: p.effect ?? '' });

export function usersOf(nodes: GraphNode[]): { node: GraphNode; user: User }[] {
  return nodes
    .filter((n) => n.namespace === ns.organisation && n.type === types.user)
    .map((node) => {
      const p = (node.props ?? {}) as Record<string, unknown>;
      return {
        node,
        user: {
          subject: String(p.subject ?? ''),
          displayName: String(p.displayName ?? ''),
          email: String(p.email ?? ''),
          locale: String(p.locale ?? ''),
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
  };
}
