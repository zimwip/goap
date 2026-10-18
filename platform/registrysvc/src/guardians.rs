//! The guardian chain of a change (ADR 0106): the methodologies that frame it, outermost
//! first, its own last, each declaring in its definition what it requires of the changes it integrates (integrates,
//! guardian). The graph saves the chain on the change and asks the registry to evaluate it.

use std::collections::{BTreeMap, HashMap, HashSet, VecDeque};
use std::sync::Arc;

use goap_condition::Object;
use goap_ctx::Ctx;
use goap_def::{Issue, Issues};
use goap_domain::{Blackboard, Change, GuardianRef, Lifecycle, split_methodology_ref};
use goap_methodology::{ANY_METHODOLOGY, Composition, Methodology};
use serde_json::Value;

use crate::changelifecycle::lifecycle_in;
use crate::error::{Error, Result};
use crate::service::Service;
use crate::store::STATUS_ARCHIVED;

/// maxChain bounds the walk up the integrations (a cycle is cut by the methodologies already seen anyway).
const MAX_CHAIN: usize = 8;

impl Service {
    /// Guardians implements graph.ChangeLifecycles: the guardian chain of a new root change of a methodology in a
    /// project. From the methodology, the methodologies applicable to the project chain (nearest project first,
    /// Applicable) are searched for one integrating it; the first found frames it, and the search goes on from that
    /// one. Without Applicable, or for a methodology the registry does not know, the chain is the methodology alone.
    pub async fn guardians(
        &self,
        ctx: &Ctx,
        r#ref: &str,
        project: &str,
    ) -> Result<Vec<GuardianRef>> {
        let (name, _) = split_methodology_ref(r#ref);
        let own = match self.record(ctx, r#ref).await {
            Err(e) if e.is_not_found() => {
                return Ok(vec![GuardianRef {
                    methodology: name.to_string(),
                    ..GuardianRef::default()
                }]);
            }
            Err(e) => return Err(e),
            Ok(r) => r,
        };
        let mut chain = vec![GuardianRef {
            methodology: name.to_string(),
            version: own.methodology.version.clone(),
        }];
        let Some(applicable) = &self.applicable else {
            return Ok(chain);
        };
        if project.is_empty() {
            return Ok(chain);
        }
        let apps = applicable(ctx.clone(), project.to_string())
            .await
            .map_err(Error::Domains)?;
        let mut seen: HashSet<String> = HashSet::from([name.to_string()]);
        let mut cur = name.to_string();
        while chain.len() < MAX_CHAIN {
            let mut found: Option<Methodology> = None;
            for a in &apps {
                if seen.contains(a) {
                    continue;
                }
                let r = match self.store.get(ctx, a, "").await {
                    Err(e) if e.is_not_found() => continue,
                    Err(e) => return Err(e),
                    Ok(r) => r,
                };
                if r.methodology.integrates_changes_of(&cur) {
                    found = Some(r.methodology);
                    break;
                }
            }
            let Some(m) = found else { break };
            chain.insert(
                0,
                GuardianRef {
                    methodology: m.name.clone(),
                    version: m.version.clone(),
                },
            );
            seen.insert(m.name.clone());
            cur = m.name;
        }
        Ok(chain)
    }

    /// Integrates implements graph.ChangeLifecycles: whether integrator (at the version its reference names, else the
    /// latest) integrates the changes of methodology; false for a methodology the registry does not know.
    pub async fn integrates(&self, ctx: &Ctx, integrator: &str, methodology: &str) -> Result<bool> {
        match self.record(ctx, integrator).await {
            Err(e) if e.is_not_found() => Ok(false),
            Err(e) => Err(e),
            Ok(r) => Ok(r.methodology.integrates_changes_of(methodology)),
        }
    }

    /// CheckGuardians implements graph.ChangeLifecycles: the outer guardians of the chain of the change, innermost
    /// first, each with its guard for the transition (guardian.transitions) or the merge (merge_guard: guardian.merge,
    /// and an attached integrator when its integration is exclusive), in the environment of the conditions of the
    /// change's own methodology plus `integrator`, the nearest ancestor change of that guardian's methodology.
    pub async fn check_guardians(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        transition: &str,
        ancestors: &[Change],
    ) -> Result<String> {
        let c = &bb.change;
        let mut world: Option<BTreeMap<String, bool>> = None;
        for i in (0..c.guardians.len()).rev() {
            let g = &c.guardians[i];
            if g.methodology == c.methodology {
                continue; // its own: the lifecycle and the merge of the change
            }
            let Some(m) = self.guardian_methodology(ctx, g).await? else {
                continue; // a guardian the registry no longer knows says nothing
            };
            let inner = match c.guardians.get(i + 1) {
                Some(n) => n.methodology.clone(),
                None => c.methodology.clone(),
            };
            let expr = if transition.is_empty() {
                m.merge_guard(&inner)
            } else {
                m.transition_guard(transition)
            };
            if expr.is_empty() {
                continue;
            }
            if world.is_none() {
                let mut w = BTreeMap::new();
                if let Ok(cm) = self.methodology(ctx, &c.methodology_ref()).await {
                    w = cm.conditions.evaluate(bb).state;
                }
                world = Some(w);
            }
            let ok = goap_condition::check_guardian(
                &expr,
                bb,
                world.as_ref(),
                transition,
                integrator_of(ancestors, &g.methodology),
            )
            .map_err(|e| Error::Other(format!("guardian {}: {e}", g.methodology)))?;
            if !ok {
                return Ok(format!("{} requires {expr}", g.methodology));
            }
        }
        Ok(String::new())
    }

    /// guardianMethodology is the definition of a guardian at the version saved in the chain, else the latest one;
    /// None when the registry knows neither.
    async fn guardian_methodology(
        &self,
        ctx: &Ctx,
        g: &GuardianRef,
    ) -> Result<Option<Methodology>> {
        if !g.version.is_empty() {
            match self.store.get(ctx, &g.methodology, &g.version).await {
                Ok(r) => return Ok(Some(r.methodology)),
                Err(e) if !e.is_not_found() => return Err(e),
                Err(_) => {}
            }
        }
        match self.store.get(ctx, &g.methodology, "").await {
            Ok(r) => Ok(Some(r.methodology)),
            Err(e) if e.is_not_found() => Ok(None),
            Err(e) => Err(e),
        }
    }

    /// integrationIssues reports a cycle of integrations (ADR 0106): m integrating, directly or through the
    /// methodologies it integrates, a methodology that integrates m. Compared with the latest version of each stored
    /// methodology; a wildcard ("*") is not followed. Nothing is reported when the store cannot be read.
    pub(crate) async fn integration_issues(&self, ctx: &Ctx, m: &Methodology) -> Issues {
        if m.integrates.is_empty() {
            return Issues::default();
        }
        let Ok(rs) = self.versions(ctx, false).await else {
            return Issues::default();
        };
        let mut integrates: HashMap<String, Vec<String>> = HashMap::new();
        for r in &rs {
            if r.methodology.name != m.name {
                integrates.insert(r.methodology.name.clone(), integrated_names(&r.methodology));
            }
        }
        integrates.insert(m.name.clone(), integrated_names(m));
        let mut out = Issues::default();
        for (i, inn) in m.integrates.iter().enumerate() {
            for n in &inn.methodologies {
                if let Some(path) = integration_path(&integrates, n, &m.name) {
                    out.0.push(Issue::new(
                        format!("integrates[{i}].methodologies"),
                        format!(
                            "integration cycle: {} integrates {}",
                            m.name,
                            path.join(" integrates ")
                        ),
                    ));
                }
            }
        }
        out
    }

    /// compositionIssues checks m against the methodologies it is composed with (ADR 0106,
    /// Methodology::composition_issues): the latest version of each other stored methodology and the lifecycles of
    /// the domains in force. Warnings only; nothing is reported when the store cannot be read.
    pub(crate) async fn composition_issues(&self, ctx: &Ctx, m: &Methodology) -> Issues {
        let Ok(rs) = self.versions(ctx, false).await else {
            return Issues::default();
        };
        let mut others: BTreeMap<String, Methodology> = BTreeMap::new();
        for r in rs {
            if r.methodology.name != m.name && r.status != STATUS_ARCHIVED {
                others.insert(r.methodology.name.clone(), r.methodology);
            }
        }
        let mut composed = !m.integrates.is_empty() || m.guardian.is_some() || !m.on.is_empty();
        for o in others.values() {
            composed = composed
                || o.integrates_changes_of(&m.name)
                || m.integrates_changes_of(&o.name)
                || o.applies_to.contains(&m.name);
        }
        if !composed {
            return Issues::default();
        }
        let Ok(domains) = self.domain_versions(ctx, false).await else {
            return Issues::default();
        };
        let domains = Arc::new(domains);
        m.composition_issues(&Composition {
            others,
            lifecycle: Some(Arc::new(move |x: &Methodology| -> Option<Lifecycle> {
                lifecycle_in(&domains, x)
            })),
        })
    }
}

/// integratorOf is what a guardian sees as `integrator`: the nearest ancestor change of its methodology (None: none,
/// guard.NoIntegrator).
fn integrator_of(ancestors: &[Change], methodology: &str) -> Option<Object> {
    let a = ancestors.iter().find(|a| a.methodology == methodology)?;
    let mut o = Object::new();
    o.insert("attached".into(), Value::Bool(true));
    o.insert("id".into(), Value::String(a.id.to_string()));
    o.insert("methodology".into(), Value::String(a.methodology.clone()));
    o.insert("state".into(), Value::String(a.state.clone()));
    o.insert("status".into(), Value::String(a.status.to_string()));
    Some(o)
}

/// integratedNames are the methodologies m names in its integrations, the wildcard left out.
fn integrated_names(m: &Methodology) -> Vec<String> {
    m.integrates
        .iter()
        .flat_map(|i| i.methodologies.iter())
        .filter(|n| n.as_str() != ANY_METHODOLOGY)
        .cloned()
        .collect()
}

/// integrationPath is a path of integrations from `from` to `to` (both included), None when there is none.
fn integration_path(
    integrates: &HashMap<String, Vec<String>>,
    from: &str,
    to: &str,
) -> Option<Vec<String>> {
    let mut prev: HashMap<String, String> = HashMap::from([(from.to_string(), String::new())]);
    let mut queue: VecDeque<String> = VecDeque::from([from.to_string()]);
    while let Some(cur) = queue.pop_front() {
        if cur == to {
            let mut path = Vec::new();
            let mut n = cur;
            while !n.is_empty() {
                path.insert(0, n.clone());
                n = prev.get(&n).cloned().unwrap_or_default();
            }
            return Some(path);
        }
        for n in integrates.get(&cur).into_iter().flatten() {
            if !prev.contains_key(n) {
                prev.insert(n.clone(), cur.clone());
                queue.push_back(n.clone());
            }
        }
    }
    None
}
