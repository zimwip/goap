//! Nodes taken out of force by their lifecycle (ADR 0076 §4c).

use std::collections::HashSet;

use goap_domain::{Link, Node, NodeID};

/// StateRetired is the state a node with no parent is taken out of force in (ADR 0076 §4c: lifecycle config of the
/// built-in domains, transition retire / restore): it is never deleted, its readers leave it out.
pub const STATE_RETIRED: &str = "retired";

/// InForce leaves out the nodes their lifecycle retired, and the links from or to them.
pub fn in_force(nodes: &[Node], links: &[Link]) -> (Vec<Node>, Vec<Link>) {
    let retired: HashSet<&NodeID> = nodes
        .iter()
        .filter(|n| n.state == STATE_RETIRED)
        .map(|n| &n.id)
        .collect();
    if retired.is_empty() {
        return (nodes.to_vec(), links.to_vec());
    }
    let out = nodes
        .iter()
        .filter(|n| n.state != STATE_RETIRED)
        .cloned()
        .collect();
    let kept = links
        .iter()
        .filter(|l| !retired.contains(&l.from.id) && !retired.contains(&l.to.id))
        .cloned()
        .collect();
    (out, kept)
}
