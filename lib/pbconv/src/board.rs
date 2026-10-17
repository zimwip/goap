use goap_domain::{self as domain, ItemID};
use goap_gen::graph::v1 as pb;

pub fn board_issue_to_pb(i: &domain::BoardIssue) -> pb::BoardIssue {
    pb::BoardIssue {
        item: i.item.to_string(),
        culprit: i.culprit.to_string(),
        code: i.code.clone(),
        message: i.message.clone(),
        severity: i.severity.clone(),
    }
}

pub fn board_issue_from_pb(i: &pb::BoardIssue) -> domain::BoardIssue {
    domain::BoardIssue {
        item: ItemID::new(i.item.clone()),
        culprit: ItemID::new(i.culprit.clone()),
        code: i.code.clone(),
        message: i.message.clone(),
        severity: i.severity.clone(),
    }
}
