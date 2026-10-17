//! The comparison of two flows of a change, impact by impact (ADR 0083).

use std::collections::HashMap;

use goap_ctx::Ctx;
use goap_domain::{
    Change, ChangeID, DIFF_ADDED, DIFF_MODIFIED, DIFF_REMOVED, FlowDiff, ImpactDiff, ImpactSide,
    LinkShape, MAIN_FLOW, NodeID, NodeRef, NodeShape, REVIEW_ACCEPTED, REVIEW_REJECTED,
    diff_shapes,
};

use crate::changeview::{VIEW_ACCEPTED, VIEW_WRITTEN};
use crate::flownodes::{FlowNodes, node_of};
use crate::lifecycle::invalidf;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// impactShape is an impact as a flow sees it, with the shape of its node.
#[derive(Debug, Clone, Default)]
pub(crate) struct ImpactShape {
    pub(crate) side: ImpactSide,
    pub(crate) node: NodeID,
    pub(crate) key: String,
    pub(crate) r#type: String,
    pub(crate) shape: NodeShape,
}

impl Graph {
    /// DiffFlows compares the impacts two flows of a change see (ADR 0083): each flow reads its impacts through its
    /// drafts (ADR 0079), the innermost draft of its chain, so a node an option did not touch is the main flow's draft
    /// on both sides. An impact is matched by node (a node created independently on both flows, with two ids, by type
    /// and key), then it is added (the right flow only), removed (the left flow only) or modified (both, with a
    /// different state, owner, property or link). Identical impacts are counted, not listed. The flows are "" or
    /// "main" for the main flow, else an option of the change (open or decided); the level is written (every impact
    /// but the rejected ones) or accepted (the accepted ones).
    pub async fn diff_flows(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        left: &str,
        right: &str,
        level: &str,
    ) -> Result<FlowDiff> {
        let level = if level.is_empty() {
            VIEW_WRITTEN
        } else {
            level
        };
        if level != VIEW_WRITTEN && level != VIEW_ACCEPTED {
            return Err(invalidf(format!(
                "flows are compared at {VIEW_WRITTEN} or {VIEW_ACCEPTED}, not {level:?}"
            )));
        }
        let mut out = FlowDiff {
            change: id.clone(),
            level: level.to_string(),
            ..Default::default()
        };
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let lf = diff_flow(&c, left)?;
        let rf = diff_flow(&c, right)?;
        out.left = flow_name(&lf);
        out.right = flow_name(&rf);
        let ls = self
            .flow_impact_shapes(ctx, &mut tx, &c, &lf, level)
            .await?;
        let rs = self
            .flow_impact_shapes(ctx, &mut tx, &c, &rf, level)
            .await?;
        match_impacts(&mut out, ls, rs);
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// flowImpactShapes reads the impacts a flow sees at a level, each with its draft as a shape (a planned one: the
    /// version it starts from, a planned creation: nothing but its key and type).
    pub(crate) async fn flow_impact_shapes(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        level: &str,
    ) -> Result<Vec<ImpactShape>> {
        let seen = FlowNodes::new(c.clone(), flow).nodes(ctx, tx).await?;
        let reader = self.new_draft_reader(ctx, tx, c, flow, &seen).await?;
        let mut keys: HashMap<NodeRef, String> = HashMap::new();
        let mut out = Vec::new();
        for cn in &seen {
            if cn.review == REVIEW_REJECTED
                || (level == VIEW_ACCEPTED && cn.review != REVIEW_ACCEPTED)
            {
                continue;
            }
            let mut is = ImpactShape {
                node: node_of(cn),
                key: cn.key.clone(),
                r#type: cn.r#type.clone(),
                side: ImpactSide {
                    impact: cn.id.clone(),
                    flow: cn.flow.clone(),
                    intent: cn.intent.clone(),
                    review: cn.review.clone(),
                    drafted: cn.post.is_some(),
                    ..Default::default()
                },
                shape: NodeShape {
                    key: cn.key.clone(),
                    r#type: cn.r#type.clone(),
                    ..Default::default()
                },
            };
            if let Some(r) = cn.post.as_ref().or(cn.pre.as_ref()) {
                let n = reader.node(ctx, tx, r).await?;
                is.key = n.key.clone();
                is.r#type = n.r#type.clone();
                is.shape = NodeShape {
                    key: n.key.clone(),
                    r#type: n.r#type.clone(),
                    state: n.state.clone(),
                    owner: n.owner.clone(),
                    properties: n.properties.clone(),
                    ..Default::default()
                };
                let links = reader.out_links(ctx, tx, r).await?;
                for l in links {
                    let to_key = match keys.get(&l.to) {
                        Some(k) => k.clone(),
                        None => {
                            let k = match reader.node(ctx, tx, &l.to).await {
                                Ok(n) => n.key,
                                Err(_) => l.to.id.to_string(),
                            };
                            keys.insert(l.to.clone(), k.clone());
                            k
                        }
                    };
                    is.shape.links.push(LinkShape {
                        r#type: l.r#type.clone(),
                        to: l.to.id.clone(),
                        to_key,
                        properties: l.properties.clone(),
                    });
                }
                is.side.state = n.state.clone();
                is.side.owner = n.owner.clone();
            }
            out.push(is);
        }
        Ok(out)
    }
}

/// diffFlow resolves a flow named by a caller: "" and "main" are the main flow, anything else an option of the change.
pub(crate) fn diff_flow(c: &Change, flow: &str) -> Result<String> {
    if flow.is_empty() || flow == MAIN_FLOW {
        return Ok(String::new());
    }
    match c.flow(flow) {
        Some(f) if f.option.is_some() => Ok(flow.to_string()),
        _ => Err(Error::wrap(
            ErrorKind::NotFound,
            format!("option {flow} of change {}", c.id),
        )),
    }
}

pub(crate) fn flow_name(flow: &str) -> String {
    if flow.is_empty() {
        MAIN_FLOW.to_string()
    } else {
        flow.to_string()
    }
}

/// matchImpacts pairs the impacts of the two flows (by node, then by type and key) and fills the diff.
pub(crate) fn match_impacts(out: &mut FlowDiff, left: Vec<ImpactShape>, right: Vec<ImpactShape>) {
    let mut by_node: HashMap<NodeID, usize> = HashMap::new();
    for (i, r) in right.iter().enumerate() {
        if !r.node.is_empty() {
            by_node.insert(r.node.clone(), i);
        }
    }
    let by_name = |s: &ImpactShape| format!("{}\x00{}", s.r#type, s.key);
    let mut right_by_name: HashMap<String, usize> = HashMap::new();
    for (i, r) in right.iter().enumerate() {
        right_by_name.insert(by_name(r), i);
    }
    let mut paired = vec![false; right.len()];
    let mut pairs: Vec<(Option<usize>, Option<usize>)> = Vec::new();
    let mut unmatched = Vec::new();
    for (i, l) in left.iter().enumerate() {
        if let Some(&j) = by_node.get(&l.node)
            && !l.node.is_empty()
            && !paired[j]
        {
            paired[j] = true;
            pairs.push((Some(i), Some(j)));
            continue;
        }
        unmatched.push(i);
    }
    for i in unmatched {
        if let Some(&j) = right_by_name.get(&by_name(&left[i]))
            && !paired[j]
        {
            paired[j] = true;
            pairs.push((Some(i), Some(j)));
            continue;
        }
        pairs.push((Some(i), None));
    }
    for (j, p) in paired.iter().enumerate() {
        if !p {
            pairs.push((None, Some(j)));
        }
    }
    for p in pairs {
        let d = match p {
            (Some(i), None) => {
                let l = &left[i];
                ImpactDiff {
                    node: l.node.clone(),
                    key: l.key.clone(),
                    r#type: l.r#type.clone(),
                    category: DIFF_REMOVED.into(),
                    left: Some(l.side.clone()),
                    ..Default::default()
                }
            }
            (None, Some(j)) => {
                let r = &right[j];
                ImpactDiff {
                    node: r.node.clone(),
                    key: r.key.clone(),
                    r#type: r.r#type.clone(),
                    category: DIFF_ADDED.into(),
                    right: Some(r.side.clone()),
                    ..Default::default()
                }
            }
            (Some(i), Some(j)) => {
                let (l, r) = (&left[i], &right[j]);
                let changes = diff_shapes(&l.shape, &r.shape);
                if changes.is_empty() {
                    out.identical += 1;
                    continue;
                }
                ImpactDiff {
                    node: r.node.clone(),
                    key: r.key.clone(),
                    r#type: r.r#type.clone(),
                    category: DIFF_MODIFIED.into(),
                    left: Some(l.side.clone()),
                    right: Some(r.side.clone()),
                    changes,
                }
            }
            (None, None) => continue,
        };
        out.impacts.push(d);
    }
    out.impacts
        .sort_by(|a, b| a.key.cmp(&b.key).then_with(|| a.r#type.cmp(&b.r#type)));
}
