use std::collections::{BTreeMap, HashMap};

use goap_domain::{self as domain, BaselineID, NodeID};
use goap_gen::graph::v1 as pb;
use goap_graphapi as graphapi;

use crate::{ref_from_pb, ref_ptr_from_pb, ref_ptr_to_pb, ref_to_pb, to_map, to_struct};

pub fn merge_plan_to_pb(p: &graphapi::MergePlan) -> pb::MergePlan {
    pb::MergePlan {
        from: p.from.clone(),
        into: p.into.clone(),
        into_head: p.into_head.to_string(),
        candidates: p
            .candidates
            .iter()
            .map(|c| pb::MergeCandidate {
                node: c.node.to_string(),
                key: c.key.clone(),
                r#type: c.r#type.clone(),
                kind: c.kind.clone(),
                ancestor: ref_ptr_to_pb(c.ancestor.as_ref()),
                ours: ref_ptr_to_pb(c.ours.as_ref()),
                theirs: Some(ref_to_pb(&c.theirs)),
                deleted: c.deleted,
                base: to_struct(&c.base),
                ours_props: to_struct(&c.ours_props),
                theirs_props: to_struct(&c.theirs_props),
                merged: to_struct(&c.merged),
                conflicts: c.conflicts.clone(),
            })
            .collect(),
    }
}

/// MergePlanFromPB is the inverse of MergePlanToPB.
pub fn merge_plan_from_pb(p: &pb::MergePlan) -> graphapi::MergePlan {
    graphapi::MergePlan {
        from: p.from.clone(),
        into: p.into.clone(),
        into_head: BaselineID::new(p.into_head.clone()),
        candidates: p
            .candidates
            .iter()
            .map(|c| graphapi::MergeCandidate {
                node: NodeID::new(c.node.clone()),
                key: c.key.clone(),
                r#type: c.r#type.clone(),
                kind: c.kind.clone(),
                ancestor: ref_ptr_from_pb(c.ancestor.as_ref()),
                ours: ref_ptr_from_pb(c.ours.as_ref()),
                theirs: ref_from_pb(c.theirs.as_ref()),
                deleted: c.deleted,
                base: to_map(c.base.as_ref()),
                ours_props: to_map(c.ours_props.as_ref()),
                theirs_props: to_map(c.theirs_props.as_ref()),
                merged: to_map(c.merged.as_ref()),
                conflicts: c.conflicts.clone(),
            })
            .collect(),
    }
}

/// ResolutionsToPB converts the resolutions of the conflicts of a merge, by node id.
pub fn resolutions_to_pb(
    rs: &BTreeMap<NodeID, graphapi::Resolution>,
) -> HashMap<String, pb::Resolution> {
    rs.iter()
        .map(|(id, r)| {
            (
                id.to_string(),
                pb::Resolution {
                    props: to_struct(&r.props),
                    skip: r.skip,
                },
            )
        })
        .collect()
}

/// BranchFromPB converts a branch (without its creation time).
pub fn branch_from_pb(b: &pb::Branch) -> domain::Branch {
    domain::Branch {
        name: b.name.clone(),
        parent: b.parent.clone(),
        fork_baseline: BaselineID::new(b.fork_baseline.clone()),
        head: BaselineID::new(b.head.clone()),
        origin: b.origin.clone(),
        status: b.status.clone(),
        namespace: b.namespace.clone(),
        description: b.description.clone(),
        rebased_from: BaselineID::new(b.rebased_from.clone()),
        ..Default::default()
    }
}
