use goap_domain::{self as domain, FlowStatus, ItemID};
use goap_gen::graph::v1 as pb;

use crate::graph::{ids_from_pb, ids_to_pb};
use crate::{from_opt_time, from_time, opt_time, time, to_map, to_struct};

pub fn flow_event_to_pb(e: &domain::FlowEvent) -> pb::FlowEvent {
    pb::FlowEvent {
        op: e.op.clone(),
        flow: e.flow.clone(),
        parent: e.parent.clone(),
        fork_after: e.fork_after.to_string(),
        stale: ids_to_pb(&e.stale),
        by: e.by.clone(),
        stale_runs: e.stale_runs.clone(),
        origin: to_struct(&e.origin),
        option: option_to_pb(e.option.as_ref()),
        comment: e.comment.clone(),
    }
}

fn option_to_pb(o: Option<&domain::OptionSpec>) -> Option<pb::Option> {
    o.map(|o| pb::Option {
        name: o.name.clone(),
        hypothesis: o.hypothesis.clone(),
    })
}

fn option_from_pb(o: Option<&pb::Option>) -> Option<domain::OptionSpec> {
    o.map(|o| domain::OptionSpec {
        name: o.name.clone(),
        hypothesis: o.hypothesis.clone(),
        ..Default::default()
    })
}

pub fn flow_event_from_pb(e: &pb::FlowEvent) -> domain::FlowEvent {
    domain::FlowEvent {
        op: e.op.clone(),
        flow: e.flow.clone(),
        parent: e.parent.clone(),
        fork_after: ItemID::new(e.fork_after.clone()),
        stale: ids_from_pb(&e.stale),
        by: e.by.clone(),
        stale_runs: e.stale_runs.clone(),
        origin: to_map(e.origin.as_ref()),
        option: option_from_pb(e.option.as_ref()),
        comment: e.comment.clone(),
    }
}

pub fn flow_to_pb(f: &domain::Flow) -> pb::Flow {
    pb::Flow {
        id: f.id.clone(),
        parent: f.parent.clone(),
        fork_after: f.fork_after.to_string(),
        origin: to_struct(&f.origin),
        status: f.status.to_string(),
        stale: ids_to_pb(&f.stale),
        opened_at: time(&f.opened_at),
        decided_at: opt_time(&f.decided_at),
        decided_by: f.decided_by.clone(),
        competes_with: f.competes_with.clone(),
        stale_runs: f.stale_runs.clone(),
        option: option_to_pb(f.option.as_ref()),
        option_status: option_status(f),
        evaluation: f.evaluation.clone(),
        active: f.active,
    }
}

fn option_status(f: &domain::Flow) -> String {
    if f.option.is_none() {
        return String::new();
    }
    f.option_status().to_string()
}

/// FlowFromPB converts a flow (the zero flow for None).
pub fn flow_from_pb(f: Option<&pb::Flow>) -> domain::Flow {
    let Some(f) = f else {
        return domain::Flow::default();
    };
    domain::Flow {
        id: f.id.clone(),
        parent: f.parent.clone(),
        fork_after: ItemID::new(f.fork_after.clone()),
        origin: to_map(f.origin.as_ref()),
        status: FlowStatus::new(f.status.clone()),
        stale: ids_from_pb(&f.stale),
        opened_at: from_time(f.opened_at.as_ref()),
        decided_at: from_opt_time(f.decided_at.as_ref()),
        decided_by: f.decided_by.clone(),
        competes_with: f.competes_with.clone(),
        stale_runs: f.stale_runs.clone(),
        option: option_from_pb(f.option.as_ref()),
        evaluation: f.evaluation.clone(),
        evaluated: f.option_status == domain::OPTION_EVALUATED,
        active: f.active,
    }
}
