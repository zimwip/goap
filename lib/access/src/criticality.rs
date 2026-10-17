//! The policy of a criticality level is organisation data (ADR 0075 §3): a CriticalityPolicy node owned by a unit says
//! what the changes held by that unit and the units below it require at one level. The nearest unit holding a policy
//! for a level wins (design rule 3), the whole policy at once; a level no unit of the chain holds takes the compiled-in
//! table (`goap_criticality::defaults`). This file is the one place that reads those nodes: nothing else names the
//! type.

use std::any::Any;
use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, TimeDelta, Utc};
use goap_criticality::{Level, Policy, Resolver};
use goap_ctx::Ctx;
use goap_domain::{Change, Node, NodeID, STRUCTURE_ORGANISATION};
use serde_json::{Map, Value};

use crate::Error;
use crate::access::{Directory, Snapshot, str_prop};
use crate::project::{str_list, to_any_list};

const NANOS_PER_HOUR: f64 = 3_600_000_000_000.0;

impl Snapshot {
    /// readCriticality reads the CriticalityPolicy nodes, each attached to its owner unit; a node that cannot be read,
    /// has no owner unit or repeats a level of its unit is a problem of the snapshot (the first one stays).
    pub(crate) fn read_criticality(&mut self, nodes: &[&Node], by_id: &HashMap<&NodeID, &Node>) {
        self.criticality = HashMap::new();
        for n in nodes {
            let owner = by_id
                .get(&n.owner)
                .filter(|o| self.structures.includes(STRUCTURE_ORGANISATION, &o.r#type));
            let Some(owner) = owner else {
                self.problems.push(format!(
                    "{}: its owner {} is not a unit of the organisation",
                    n.key, n.owner
                ));
                continue;
            };
            let (lvl, p) = match criticality_policy_from_props(&n.properties) {
                Ok(x) => x,
                Err(e) => {
                    self.problems.push(format!("{}: {e}", n.key));
                    continue;
                }
            };
            let unit = self.criticality.entry(owner.key.clone()).or_default();
            if unit.contains_key(&lvl) {
                self.problems.push(format!(
                    "{}: unit {} already has a policy for {lvl}",
                    n.key, owner.key
                ));
                continue;
            }
            unit.insert(lvl, p);
        }
    }

    /// CriticalityPolicy is what the level requires of the changes held by the unit: the policy of the nearest unit of
    /// its chain (the unit, its ancestors, the root) that holds one for the level, else the compiled-in table.
    pub fn criticality_policy(&self, unit: &str, l: &Level) -> Policy {
        for u in self.chain(unit) {
            if let Some(p) = self.criticality.get(&u).and_then(|m| m.get(l)) {
                return p.clone();
            }
        }
        default_policy(l)
    }

    /// CriticalityOf is the policy of the criticality level l for a change: resolved from the unit that holds it.
    pub fn criticality_of(&self, c: &Change, l: &Level) -> Policy {
        self.criticality_policy(&c.owner_org, l)
    }
}

fn default_policy(l: &Level) -> Policy {
    goap_criticality::defaults().remove(l).unwrap_or_default()
}

/// The policy of a level for a change from a snapshot, the compiled-in table without one (Go's nil snapshot).
pub fn criticality_of(s: Option<&Snapshot>, c: &Change, l: &Level) -> Policy {
    match s {
        Some(s) => s.criticality_of(c, l),
        None => default_policy(l),
    }
}

/// A number as Go's encoding/json writes a float64: an integer when it has no fraction.
fn go_float(f: f64) -> Value {
    if f.fract() == 0.0 && f.abs() < 9.0e15 {
        Value::from(f as i64)
    } else {
        Value::from(f)
    }
}

/// CriticalityPolicyFromProps reads the level and the policy of a CriticalityPolicy node.
pub fn criticality_policy_from_props(props: &Map<String, Value>) -> Result<(Level, Policy), Error> {
    let lvl = Level::new(str_prop(props, "level"));
    if !goap_criticality::valid(&lvl) {
        return Err(Error::Invalid(format!(
            "criticality policy: unknown level {:?}",
            lvl.as_str()
        )));
    }
    let oracles = str_list(props, "oracles")
        .map_err(|e| Error::Invalid(format!("criticality policy: {e}")))?;
    let mut p = Policy {
        oracles,
        signatory_role: str_prop(props, "signatoryRole"),
        sampling: props
            .get("sampling")
            .and_then(Value::as_bool)
            .unwrap_or(false),
        ..Policy::default()
    };
    let hours = match props.get("maxDerogationHours") {
        None | Some(Value::Null) => 0.0,
        Some(Value::Number(n)) => n.as_f64().unwrap_or(0.0),
        Some(_) => {
            return Err(Error::Invalid(
                "criticality policy: maxDerogationHours must be a number".into(),
            ));
        }
    };
    if hours < 0.0 {
        return Err(Error::Invalid(
            "criticality policy: maxDerogationHours must not be negative".into(),
        ));
    }
    p.max_derogation = TimeDelta::nanoseconds((hours * NANOS_PER_HOUR) as i64);
    Ok((lvl, p))
}

/// CriticalityProps returns the properties of a CriticalityPolicy node.
pub fn criticality_props(l: &Level, p: &Policy) -> Map<String, Value> {
    let mut m = Map::new();
    m.insert("level".into(), l.as_str().into());
    m.insert("sampling".into(), p.sampling.into());
    let ns = p.max_derogation.num_nanoseconds().unwrap_or(i64::MAX) as f64;
    m.insert("maxDerogationHours".into(), go_float(ns / NANOS_PER_HOUR));
    if !p.oracles.is_empty() {
        m.insert("oracles".into(), to_any_list(&p.oracles));
    }
    if !p.signatory_role.is_empty() {
        m.insert("signatoryRole".into(), p.signatory_role.clone().into());
    }
    m
}

/// CriticalityPolicyKey is the key of the CriticalityPolicy node of a unit for a level.
pub fn criticality_policy_key(unit: &str, l: &Level) -> String {
    format!("CRIT:{unit}/{l}")
}

/// The `goap_criticality::Resolver` of a directory ([`Directory::criticality_resolver`]).
#[derive(Debug, Clone)]
pub struct CriticalityResolver(pub Arc<Directory>);

#[async_trait]
impl Resolver for CriticalityResolver {
    async fn resolve(&self, ctx: &Ctx, c: &Change, l: &Level) -> Policy {
        criticality_of(self.0.snapshot_or_last(ctx).await.as_deref(), c, l)
    }
}

/// A provider of a facet of the blackboard (`Graph.Facets`): the value of the facet for a change, at a time; no I/O.
pub type FacetProvider =
    Arc<dyn Fn(&Change, DateTime<Utc>) -> Arc<dyn Any + Send + Sync> + Send + Sync>;

impl Directory {
    /// CriticalityResolver is the `goap_criticality::Resolver` of the directory: it reads the current snapshot (the
    /// compiled-in table when the graph cannot be read).
    pub fn criticality_resolver(self: &Arc<Self>) -> Arc<dyn Resolver> {
        Arc::new(CriticalityResolver(self.clone()))
    }

    /// CriticalityFacet is the provider of the facet of the blackboard that gives conditions and guards the policy of
    /// the level of the change (`FACET_CRITICALITY_POLICY`, Graph.Facets): it reads the snapshot last built and never
    /// the graph, as a facet does no I/O. Before any snapshot, or when none was built, the compiled-in table applies.
    /// The facet value is a `goap_criticality::Policy`.
    pub fn criticality_facet(self: &Arc<Self>) -> FacetProvider {
        let d = self.clone();
        Arc::new(move |c: &Change, _now: DateTime<Utc>| {
            let p = criticality_of(d.peek().as_deref(), c, &goap_criticality::of(&c.data));
            Arc::new(p) as Arc<dyn Any + Send + Sync>
        })
    }
}
