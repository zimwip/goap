//! Port of `bootstrap.go`.

use std::collections::HashMap;
use std::sync::atomic::Ordering;

use goap_ctx::Ctx;
use goap_domain::{
    BASELINE_SNAPSHOT, Baseline, BaselineID, CHANGE_APPLIED, Change, ChangeID, Link, LinkID,
    MAIN_BRANCH, Node, NodeID, REASON_CREATE, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT,
};

use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// Bootstrap (ADR 0054) creates the roots of the structures the domains declare (the root organisational unit and
    /// the root project of the built-in organisation domain, with the initial properties its tags give them), when the
    /// graph has none of them: every change is held by a unit and acts in a project, and every node version is owned
    /// by a unit and was created in a project, so the first change needs both to exist already. They are written in
    /// one transaction by one applied change per namespace (the structures may live in different namespaces), held by
    /// the root unit and acting in the root project it creates: the root unit owns itself and the root project, both
    /// were created in the root project. Everything else, seeds included, is an ordinary change after it.
    ///
    /// It is idempotent: a graph that has every root is left alone; a graph that has only some of them is refused.
    /// The graph runs it before the first change it opens (CreateChange); services call it at start, before serving.
    pub async fn bootstrap(&self, ctx: &Ctx) -> Result<()> {
        if self.booted.load(Ordering::Acquire) {
            return Ok(());
        }
        let mut res = self.bootstrap_once(ctx).await;
        if matches!(&res, Err(e) if e.is_conflict()) {
            // another process bootstrapped meanwhile (the keys are unique): check it did
            res = self.bootstrap_once(ctx).await;
        }
        if res.is_ok() {
            self.booted.store(true, Ordering::Release);
        }
        res
    }

    async fn bootstrap_once(&self, ctx: &Ctx) -> Result<()> {
        let mut tx = self.begin(ctx).await?;
        self.bootstrap_tx(ctx, &mut tx).await?;
        tx.commit(ctx).await
    }

    async fn bootstrap_tx(&self, ctx: &Ctx, tx: &mut dyn Tx) -> Result<()> {
        let roots = self.bootstrap_structures();
        // the owner axis and the project axis every node version is stamped with
        let (org, proj) = (roots[0].clone(), roots[1].clone());
        if org.root.is_empty() || proj.root.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                "no domain tags the organisation and the project structures",
            ));
        }
        let mut have = 0;
        for st in &roots {
            match tx.node_id_by_key(ctx, &st.namespace, &st.root).await {
                Ok(_) => have += 1,
                Err(e) if e.is_not_found() => {}
                Err(e) => return Err(e),
            }
        }
        if have == roots.len() {
            return Ok(());
        }
        if have != 0 {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "the graph has only some of its roots ({have} of {})",
                    roots.len()
                ),
            ));
        }
        let now = self.now();
        let mut ids: HashMap<String, NodeID> = HashMap::new();
        for st in &roots {
            ids.insert(st.kind.clone(), NodeID::new(self.new_id()));
        }
        // one change per namespace the roots live in
        let mut changes: HashMap<String, Change> = HashMap::new();
        let mut bases: HashMap<String, Baseline> = HashMap::new();
        let mut namespaces: Vec<String> = Vec::new();
        for st in &roots {
            if changes.contains_key(&st.namespace) {
                continue;
            }
            namespaces.push(st.namespace.clone());
            // the state the change starts from: the head of main, the empty state when no change landed yet (ADR
            // 0056)
            let base = branch_head(ctx, tx, &st.namespace, MAIN_BRANCH).await?;
            let c = Change {
                id: ChangeID::new(self.new_id()),
                title: "Bootstrap".into(),
                namespace: st.namespace.clone(),
                status: CHANGE_APPLIED,
                intent: "Create the roots of the structures every change and node version is placed in (ADR 0054)"
                    .into(),
                baseline_id: base.id.clone(),
                branch: MAIN_BRANCH.into(),
                owner_org: org.root.clone(),
                project_id: proj.root.clone(),
                created_at: now,
                ..Default::default()
            };
            bases.insert(st.namespace.clone(), base);
            tx.put_change(ctx, c.clone()).await?;
            changes.insert(st.namespace.clone(), c);
        }
        for st in &roots {
            let c = &changes[&st.namespace];
            let n = Node {
                id: ids[&st.kind].clone(),
                version: 1,
                branch: MAIN_BRANCH.into(),
                reason: REASON_CREATE.into(),
                namespace: st.namespace.clone(),
                key: st.root.clone(),
                r#type: st.r#type.clone(),
                properties: st.bootstrap.clone(),
                change_id: c.id.clone(),
                comment: c.intent.clone(),
                owner: ids[STRUCTURE_ORGANISATION].clone(),
                project: ids[STRUCTURE_PROJECT].clone(),
                created_at: now,
                ..Default::default()
            };
            let r = n.r#ref();
            tx.put_node(ctx, n).await?;
            if st.self_parent {
                tx.put_link(
                    ctx,
                    Link {
                        id: LinkID::new(self.new_id()),
                        r#type: st.parent.clone(),
                        from: r.clone(),
                        to: r,
                        change_id: c.id.clone(),
                        ..Default::default()
                    },
                )
                .await?;
            }
        }
        for ns in &namespaces {
            let base = &bases[ns];
            let mut nodes = base.nodes.clone();
            for st in &roots {
                if &st.namespace == ns {
                    nodes.insert(ids[&st.kind].clone(), 1);
                }
            }
            let mut c = changes[ns].clone();
            let res = Baseline {
                id: BaselineID::new(self.new_id()),
                name: "Bootstrap".into(),
                namespace: ns.clone(),
                branch: MAIN_BRANCH.into(),
                parent_id: base.id.clone(),
                change_id: c.id.clone(),
                kind: BASELINE_SNAPSHOT.into(),
                nodes,
                created_at: now,
                ..Default::default()
            };
            tx.put_baseline(ctx, res.clone()).await?;
            self.advance_branch(ctx, tx, ns, MAIN_BRANCH, &res).await?;
            c.result_baseline_id = res.id;
            tx.put_change(ctx, c).await?;
        }
        Ok(())
    }
}
