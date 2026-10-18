//! The use case: open, update, discard and submit a review of a change.

use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{self as domain, Change, ChangeID, ChangeImpact, ChangeItem};

use crate::review::{
    ACCEPT, DISCARDED, Entry, Error, KIND_REVIEW, OPEN, REJECT, Review, SUBMITTED, awaiting,
    data_of, effective_comment, find, reviews, status_on,
};

/// Port is what the operations need of the graph: the change (with its items), the write of items, and the batch of
/// reviews that applies a submission atomically (the graph and its client implement it). Its refusals are carried
/// as [`Error::Port`].
#[async_trait]
pub trait Port: Send + Sync {
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, Error>;
    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<ChangeItem>,
    ) -> Result<Vec<ChangeItem>, Error>;
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: domain::ReviewBatch,
    ) -> Result<Vec<ChangeImpact>, Error>;
}

/// Actor is who acts on a review: the principal's subject, and whether it may act on the reviews of others (an
/// administrator; the caller decides, the use case knows no role).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Actor {
    pub subject: String,
    pub admin: bool,
}

/// adminRole is the platform role of an administrator (access.RoleAdmin; this crate cannot depend on lib/access).
const ADMIN_ROLE: &str = "admin";

/// ActorOf is the actor of a call: the principal of the context, an administrator when it holds the admin role or is
/// a platform service. A service that knows more (the floor of the authorizer) sets Admin itself.
pub fn actor_of(ctx: &Ctx) -> Actor {
    let p = goap_authz::principal_from(ctx);
    Actor {
        admin: p.is_system() || p.roles.iter().any(|r| r == ADMIN_ROLE),
        subject: p.subject,
    }
}

/// Edit is a change to an open review. Comment replaces the global comment when set; Remove then Add change the set
/// of impacts (an impact added joins with no outcome); Entries set the comment / outcome of entries, after the set
/// changed.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Edit {
    pub comment: Option<String>,
    pub remove: Vec<String>,
    pub add: Vec<String>,
    pub entries: Vec<EntryEdit>,
}

/// EntryEdit sets the comment and / or the outcome of the entry of an impact.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct EntryEdit {
    pub impact: String,
    pub comment: Option<String>,
    /// accept, reject, or "" to take the decision back
    pub outcome: Option<String>,
}

/// The clock of a [`Service`].
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;
/// The key generator of a [`Service`].
pub type KeyGen = Arc<dyn Fn() -> String + Send + Sync>;

/// Service is the use case: it reads the reviews of a change, checks them and writes them as items.
#[derive(Clone)]
pub struct Service {
    pub port: Arc<dyn Port>,
    /// Now and NewKey are the clock and the key generator; None: the current time and a random key.
    pub now: Option<Clock>,
    pub new_key: Option<KeyGen>,
}

/// flowArg is the flow a call names for a resolved flow: the main flow explicitly, so that no active option takes it.
fn flow_arg(flow: &str) -> String {
    if flow.is_empty() {
        domain::MAIN_FLOW.to_string()
    } else {
        flow.to_string()
    }
}

fn invalid(msg: String) -> Error {
    Error::Invalid(msg)
}

impl Service {
    /// A service over a port, with the default clock and key generator.
    pub fn new(port: Arc<dyn Port>) -> Self {
        Service {
            port,
            now: None,
            new_key: None,
        }
    }

    fn now(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    fn new_key(&self) -> String {
        if let Some(f) = &self.new_key {
            return f();
        }
        let b = uuid::Uuid::new_v4();
        let hex: String = b.as_bytes()[..5]
            .iter()
            .map(|x| format!("{x:02X}"))
            .collect();
        format!("REV-{hex}")
    }

    /// Open starts a review on a flow ("": the active option, else the main flow) with its global comment.
    pub async fn open(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        flow: &str,
        comment: &str,
        who: &Actor,
    ) -> Result<Review, Error> {
        let c = self.port.change(ctx, change).await?;
        let flow = c.resolve_flow(flow);
        let r = Review {
            key: self.new_key(),
            flow,
            comment: comment.trim().to_string(),
            status: OPEN.into(),
            by: who.subject.clone(),
            entries: Vec::new(),
            ..Default::default()
        };
        self.write(ctx, change, r, who).await
    }

    /// Update changes an open review: only its author or an administrator, and only while it is open. The impacts it
    /// adds must await review on its flow and be in no other open review of the flow.
    pub async fn update(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        key: &str,
        e: Edit,
        who: &Actor,
    ) -> Result<Review, Error> {
        let (c, mut r) = self.load(ctx, change, key, who).await?;
        if let Some(cm) = &e.comment {
            r.comment = cm.trim().to_string();
        }
        for imp in &e.remove {
            if r.entry(imp).is_none() {
                return Err(invalid(format!("impact {imp} is not in review {key}")));
            }
            r.entries.retain(|x| x.impact != *imp);
        }
        if !e.add.is_empty() {
            let rs = reviews(&c.items);
            let awaiting: std::collections::HashSet<String> = awaiting(&c, &r.flow, &rs, key)
                .into_iter()
                .map(|cn| cn.id.into_string())
                .collect();
            for imp in &e.add {
                if r.entry(imp).is_some() {
                    continue; // already in: adding is idempotent
                }
                if !awaiting.contains(imp) {
                    return Err(why_not_awaiting(&c, &r.flow, &rs, key, imp));
                }
                r.entries.push(Entry {
                    impact: imp.clone(),
                    ..Default::default()
                });
            }
        }
        for ee in &e.entries {
            let Some(i) = r.entries.iter().position(|x| x.impact == ee.impact) else {
                return Err(invalid(format!(
                    "impact {} is not in review {key}",
                    ee.impact
                )));
            };
            if let Some(cm) = &ee.comment {
                r.entries[i].comment = cm.trim().to_string();
            }
            if let Some(o) = &ee.outcome {
                if !o.is_empty() && o != ACCEPT && o != REJECT {
                    return Err(invalid(format!(
                        "outcome of {} must be {ACCEPT} or {REJECT}",
                        ee.impact
                    )));
                }
                r.entries[i].outcome = o.clone();
            }
        }
        self.write(ctx, change, r, who).await
    }

    /// Discard drops an open review: a review that was never submitted stays in the log, final.
    pub async fn discard(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        key: &str,
        who: &Actor,
    ) -> Result<Review, Error> {
        let (_, mut r) = self.load(ctx, change, key, who).await?;
        r.status = DISCARDED.into();
        self.write(ctx, change, r, who).await
    }

    /// Submit applies every entry as a review of its change impact, by the submitter, with the entry's comment and
    /// the global one: in one transaction, all of them or none. A refusal names the entry and leaves the review open.
    /// The review item becomes submitted, final.
    pub async fn submit(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        key: &str,
        who: &Actor,
    ) -> Result<Review, Error> {
        let (_, mut r) = self.load(ctx, change, key, who).await?;
        if r.entries.is_empty() {
            return Err(invalid(format!("review {key} has no entry")));
        }
        let mut b = domain::ReviewBatch {
            id: key.to_string(),
            flow: flow_arg(&r.flow),
            by: who.subject.clone(),
            ..Default::default()
        };
        for e in &r.entries {
            if !e.decided() {
                return Err(invalid(format!("impact {} has no outcome yet", e.impact)));
            }
            let status = if e.outcome == REJECT {
                domain::REVIEW_REJECTED
            } else {
                domain::REVIEW_ACCEPTED
            };
            let comment = effective_comment(&r.comment, &e.comment);
            if comment.is_empty() {
                return Err(invalid(format!(
                    "impact {} needs a comment, its own or the review's",
                    e.impact
                )));
            }
            b.verdicts.push(domain::ImpactVerdict {
                impact: e.impact.as_str().into(),
                status,
                comment,
            });
        }
        r.status = SUBMITTED.into();
        r.submitted_at = Some(self.now());
        b.item = Some(ChangeItem {
            r#type: KIND_REVIEW,
            flow: flow_arg(&r.flow),
            data: data_of(&r),
            produced_by: who.subject.clone(),
            ..Default::default()
        });
        self.port.impact_node_review_batch(ctx, change, b).await?;
        let c = self.port.change(ctx, change).await?;
        Ok(find(&c.items, key).unwrap_or_default())
    }

    /// load reads a review for a change of it: it must exist, be open and be the actor's (or the actor an
    /// administrator).
    async fn load(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        key: &str,
        who: &Actor,
    ) -> Result<(Change, Review), Error> {
        let c = self.port.change(ctx, change).await?;
        let Some(r) = find(&c.items, key) else {
            return Err(Error::NotFound(format!("review {key} of change {change}")));
        };
        if !who.admin && who.subject != r.by {
            return Err(Error::Forbidden(format!(
                "review {key} belongs to {}",
                r.by
            )));
        }
        if !r.open() {
            return Err(Error::Conflict(format!(
                "review {key} is {}: it is final",
                r.status
            )));
        }
        Ok((c, r))
    }

    /// write stores a version of the review as an item and returns what the change then folds it to.
    async fn write(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        r: Review,
        who: &Actor,
    ) -> Result<Review, Error> {
        let it = ChangeItem {
            r#type: KIND_REVIEW,
            flow: flow_arg(&r.flow),
            data: data_of(&r),
            produced_by: who.subject.clone(),
            ..Default::default()
        };
        let items = self.port.add_items(ctx, change, vec![it]).await?;
        // the review as the item put it: the write is the truth
        let mut out = reviews(&items);
        if out.is_empty() {
            return Ok(r);
        }
        let mut got = out.swap_remove(0);
        got.versions = r.versions + 1;
        Ok(got)
    }
}

/// whyNotAwaiting is the refusal of an impact that cannot join a review.
fn why_not_awaiting(c: &Change, flow: &str, reviews: &[Review], key: &str, impact: &str) -> Error {
    let Some(cn) = c.nodes.iter().find(|cn| cn.id.as_str() == impact) else {
        return Error::NotFound(format!("change impact {impact} is not in change {}", c.id));
    };
    for o in reviews {
        if o.open() && o.flow == flow && o.key != key && o.entry(impact).is_some() {
            return Error::Conflict(format!(
                "change impact {impact} ({}) is already in the open review {}",
                cn.key, o.key
            ));
        }
    }
    Error::Conflict(format!(
        "change impact {impact} ({}) does not await review on this flow ({})",
        cn.key,
        status_on(cn, flow)
    ))
}
