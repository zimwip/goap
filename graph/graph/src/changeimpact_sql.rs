//! The flat storage form of a change impact shared by the SQL backends.

use goap_domain::{
    ChangeImpact, ChangeImpactID, ItemID, NodeID, NodeIntent, NodeRef, NodeReview, Review,
};
use serde_json::Value;

use crate::Result;

/// CnRow is the flat storage form of a change impact shared by the SQL backends.
#[derive(Debug, Clone, Default)]
pub(crate) struct CnRow {
    pub id: String,
    pub node_id: Option<String>,
    pub pre: Option<i64>,
    pub post: Option<i64>,
    pub landed: Option<i64>,
    pub reviews: Value,
    pub derived_from: Value,
    pub items: Value,
    pub key: String,
    pub r#type: String,
    pub intent: String,
    pub review: String,
    pub rationale: String,
    pub via: String,
    pub recheck: bool,
    pub produced_by: String,
    pub execution: String,
    pub flow: String,
    pub superseded: bool,
}

/// toCNRow flattens a change impact (its lists as JSON arrays, `[]` when empty).
pub(crate) fn to_cn_row(cn: &ChangeImpact) -> Result<CnRow> {
    let ver = |r: &Option<NodeRef>| r.as_ref().map(|r| r.version);
    let node_id = match (&cn.pre, &cn.post) {
        (Some(p), _) => Some(p.id.to_string()),
        (None, Some(p)) => Some(p.id.to_string()),
        _ => None,
    };
    Ok(CnRow {
        id: cn.id.to_string(),
        node_id,
        pre: ver(&cn.pre),
        post: ver(&cn.post),
        landed: ver(&cn.landed),
        reviews: serde_json::to_value(&cn.reviews)?,
        derived_from: serde_json::to_value(&cn.derived_from)?,
        items: serde_json::to_value(&cn.items)?,
        key: cn.key.clone(),
        r#type: cn.r#type.clone(),
        intent: cn.intent.to_string(),
        review: cn.review.to_string(),
        rationale: cn.rationale.clone(),
        via: cn.via.to_string(),
        recheck: cn.recheck,
        produced_by: cn.produced_by.clone(),
        execution: cn.execution.clone(),
        flow: cn.flow.clone(),
        superseded: cn.superseded,
    })
}

impl CnRow {
    /// node rebuilds the change impact (its creation time aside).
    pub(crate) fn node(self) -> Result<ChangeImpact> {
        let node_id = self.node_id.clone();
        let r = |v: Option<i64>| -> Option<NodeRef> {
            match (v, &node_id) {
                (Some(v), Some(id)) => Some(NodeRef {
                    id: NodeID::new(id.clone()),
                    version: v,
                }),
                _ => None,
            }
        };
        let list = |v: Value| -> Value { if v.is_null() { Value::Array(vec![]) } else { v } };
        let reviews: Vec<Review> = serde_json::from_value(list(self.reviews))?;
        let derived_from: Vec<ItemID> = serde_json::from_value(list(self.derived_from))?;
        let items: Vec<ItemID> = serde_json::from_value(list(self.items))?;
        Ok(ChangeImpact {
            id: ChangeImpactID::new(self.id),
            key: self.key,
            r#type: self.r#type,
            intent: NodeIntent::new(self.intent),
            review: NodeReview::new(self.review),
            rationale: self.rationale,
            via: ChangeImpactID::new(self.via),
            recheck: self.recheck,
            produced_by: self.produced_by,
            execution: self.execution,
            flow: self.flow,
            superseded: self.superseded,
            pre: r(self.pre),
            post: r(self.post),
            landed: r(self.landed),
            reviews,
            derived_from,
            items,
            ..Default::default()
        })
    }
}
