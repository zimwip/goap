//! The roots of the structures (Go's `Graph.Bootstrap`, ADR 0054): the root unit and the root project, written by one
//! applied change per namespace they live in, before any other change.
use std::collections::BTreeMap;

use graph_catalog::catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};

use super::{change::STATUS_APPLIED, tx::Tx};
use crate::{
    GraphError, Result,
    change::Change,
    model::{Baseline, BaselineId, ChangeId, Link, LinkId, MAIN_BRANCH, Node, NodeId, REASON_CREATE, Target},
    read,
    store::WriteTx,
};

/// The kind of the baseline the bootstrap leaves (`domain.BaselineSnapshot`).
const BASELINE_SNAPSHOT: &str = "snapshot";

impl<S: WriteTx> Tx<'_, S> {
    /// Creates the roots the structures of the catalogue declare when the graph has none of them, with the initial
    /// properties their tags give them: the root unit owns itself and the root project, both created in the root
    /// project, by one applied change per namespace, titled "Bootstrap". A graph holding every root is left alone
    /// (false), one holding only some of them is refused.
    pub async fn bootstrap(&mut self) -> Result<bool> {
        let cat = self.catalog();
        let (Some(org), Some(proj)) = (
            cat.structure(STRUCTURE_ORGANISATION).cloned(),
            cat.structure(STRUCTURE_PROJECT).cloned(),
        ) else {
            return Err(GraphError::invalid(
                "no domain tags the organisation and the project structures",
            ));
        };
        if org.root.is_empty() || proj.root.is_empty() {
            return Err(GraphError::invalid(
                "no domain tags the organisation and the project structures",
            ));
        }
        let roots = [org, proj];
        let mut have = 0;
        for st in &roots {
            if self.store.node_id_by_key(&st.namespace, &st.root).await?.is_some() {
                have += 1;
            }
        }
        match have {
            0 => {}
            n if n == roots.len() => return Ok(false),
            n => {
                return Err(GraphError::conflict(format!(
                    "the graph has only some of its roots ({n} of {})",
                    roots.len()
                )));
            }
        }
        let now = self.env.now();
        let owner = self.structure_url(STRUCTURE_ORGANISATION, &roots[0].root)?;
        let project = self.structure_url(STRUCTURE_PROJECT, &roots[1].root)?;
        let ids: Vec<NodeId> = roots.iter().map(|_| NodeId(self.env.new_id())).collect();
        // one change per namespace the roots live in, starting from the head of main (the empty state at first)
        let mut namespaces: Vec<String> = Vec::new();
        let mut changes: BTreeMap<String, (Change, Baseline)> = BTreeMap::new();
        for st in &roots {
            if changes.contains_key(&st.namespace) {
                continue;
            }
            namespaces.push(st.namespace.clone());
            let base = read::branch_head(self.store, &st.namespace, MAIN_BRANCH).await?;
            let c = Change {
                id: ChangeId(self.env.new_id()),
                title: "Bootstrap".into(),
                namespace: st.namespace.clone(),
                status: STATUS_APPLIED.into(),
                intent: "Create the roots of the structures every change and node version is placed in (ADR 0054)"
                    .into(),
                baseline_id: base.id.clone(),
                branch: MAIN_BRANCH.into(),
                owner_org: owner.clone(),
                project_id: project.clone(),
                created_at: Some(now),
                ..Change::default()
            };
            self.put_change(&c).await?;
            changes.insert(st.namespace.clone(), (c, base));
        }
        for (st, id) in roots.iter().zip(&ids) {
            let c = &changes[&st.namespace].0;
            let n = Node {
                id: id.clone(),
                version: 1,
                branch: MAIN_BRANCH.into(),
                reason: REASON_CREATE.into(),
                namespace: st.namespace.clone(),
                key: st.root.clone(),
                typ: st.typ.clone(),
                props: Some(st.bootstrap.clone()),
                change_id: c.id.clone(),
                comment: c.intent.clone(),
                owner: owner.clone(),
                project: project.clone(),
                created_at: Some(now),
                ..Node::default()
            };
            let change_id = c.id.clone();
            self.put_node(&n).await?;
            if st.self_parent {
                let l = Link {
                    id: LinkId(self.env.new_id()),
                    typ: st.parent.clone(),
                    from: n.node_ref(),
                    to: Target::Node(n.node_ref()),
                    props: None,
                    change_id,
                };
                self.put_links(&[l]).await?;
            }
        }
        for ns in &namespaces {
            let (mut c, base) = changes[ns].clone();
            let delta: BTreeMap<NodeId, i32> = roots
                .iter()
                .zip(&ids)
                .filter(|(st, _)| &st.namespace == ns)
                .map(|(_, id)| (id.clone(), 1))
                .collect();
            let res = Baseline {
                id: BaselineId(self.env.new_id()),
                name: "Bootstrap".into(),
                namespace: ns.clone(),
                branch: MAIN_BRANCH.into(),
                parent_id: base.id.clone(),
                change_id: c.id.clone(),
                kind: BASELINE_SNAPSHOT.into(),
                created_at: Some(now),
                ..Baseline::default()
            };
            let res = self.put_baseline(&res, Some(delta)).await?;
            self.advance_branch(ns, MAIN_BRANCH, &res).await?;
            c.result_baseline_id = res.id.clone();
            self.put_change(&c).await?;
        }
        Ok(true)
    }
}
