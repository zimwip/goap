//! The composition of the bindings of the engine with the view of the registry.

use std::collections::HashMap;

use goap_gen::engine::v1::ListStartingPointsResponse;
use goap_gen::registry::v1::{MethodologyView, ViewActivity, ViewStep};
use serde::{Deserialize, Serialize};

/// A step of a process.
pub const KIND_STEP: &str = "step";
/// The method a step names as a capability, or a step of that method.
pub const KIND_METHOD: &str = "method";

/// Launch is what starts a point: the fields of StartProcess running exactly it.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Launch {
    pub methodology: String,
    pub agent: String,
    pub goal: String,
    #[serde(rename = "changeId")]
    pub change_id: String,
}

/// Point is a possible step as it reads.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Point {
    pub id: String,
    pub kind: String,
    /// Process is the process (or method) the step belongs to; Parent, for a step of a method, the step of the process
    /// that names the capability.
    pub process: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub parent: String,
    pub name: String,
    /// Method and Capability: the method that applies and the capability it provides.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub method: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub capability: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub guidance: String,
    /// Why are the entry conditions that hold ("name" / "!name"); Produces the exit criteria the step reaches.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub why: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub produces: Vec<String>,
    /// Responsible and Accountable are the roles in force for the step (ADR 0035 §2).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub responsible: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub accountable: String,
    pub launch: Launch,
    #[serde(rename = "mayRun")]
    pub may_run: bool,
    #[serde(rename = "needRoles", default, skip_serializing_if = "Vec::is_empty")]
    pub need_roles: Vec<String>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub running: bool,
}

impl Point {
    /// Possible reports whether the caller may start the point now.
    pub fn possible(&self) -> bool {
        self.may_run && !self.running
    }
}

/// Blocked is a step towards the goal that waits for conditions.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Blocked {
    pub id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    /// Encoded `null` when empty, like Go's nil slice.
    #[serde(serialize_with = "null_if_empty", deserialize_with = "null_or_list")]
    pub missing: Vec<String>,
}

/// Points is the composed answer.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Points {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub goal: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reason: String,
    pub points: Vec<Point>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub blocked: Vec<Blocked>,
    #[serde(rename = "blockedCount", default, skip_serializing_if = "is_zero")]
    pub blocked_count: i64,
}

fn null_if_empty<S: serde::Serializer>(v: &[String], s: S) -> Result<S::Ok, S::Error> {
    if v.is_empty() {
        s.serialize_none()
    } else {
        v.serialize(s)
    }
}

fn null_or_list<'de, D: serde::Deserializer<'de>>(d: D) -> Result<Vec<String>, D::Error> {
    Ok(Option::<Vec<String>>::deserialize(d)?.unwrap_or_default())
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

impl Points {
    /// Find returns the point of an id.
    pub fn find(&self, id: &str) -> Option<&Point> {
        self.points.iter().find(|p| p.id == id)
    }
}

/// Compose reads the points the engine bound (b) in the view of their methodology version (v, None: the points keep
/// their ids and bindings only, a step the view does not hold too).
pub fn compose(v: Option<&MethodologyView>, b: &ListStartingPointsResponse) -> Points {
    let mut steps: HashMap<&str, &ViewStep> = HashMap::new();
    let mut methods: HashMap<&str, &ViewActivity> = HashMap::new();
    fn index<'a>(steps: &mut HashMap<&'a str, &'a ViewStep>, list: &'a [ViewStep]) {
        for s in list {
            steps.insert(&s.path, s);
            index(steps, &s.steps);
        }
    }
    if let Some(v) = v {
        for a in &v.processes {
            index(&mut steps, &a.steps);
        }
        for a in &v.methods {
            methods.insert(&a.name, a);
            index(&mut steps, &a.steps);
        }
    }
    let mut out = Points {
        methodology: b.methodology.clone(),
        version: b.version.clone(),
        goal: b.goal.clone(),
        reason: b.reason.clone(),
        points: Vec::new(),
        blocked: Vec::new(),
        blocked_count: i64::from(b.blocked_count),
    };
    for p in &b.points {
        let l = p.launch.clone().unwrap_or_default();
        let mut pt = Point {
            id: p.id.clone(),
            kind: p.kind.clone(),
            parent: p.parent.clone(),
            method: p.method.clone(),
            why: p.why.clone(),
            may_run: p.may_run,
            need_roles: p.need_roles.clone(),
            running: p.running,
            launch: Launch {
                methodology: l.methodology,
                agent: l.agent,
                goal: l.goal,
                change_id: l.change_id,
            },
            ..Default::default()
        };
        let s = steps.get(pt.id.as_str()).copied();
        let me = methods.get(pt.method.as_str()).copied();
        if let Some(s) = s {
            pt.process = s.owner.clone();
            pt.name = s.name.clone();
            pt.description = s.description.clone();
            pt.guidance = s.guidance.clone();
            pt.produces = conditions(&s.exit);
        }
        if pt.kind == KIND_METHOD
            && pt.parent.is_empty()
            && let Some(s) = s
        {
            pt.capability = s.capability.clone(); // the step naming the capability, proposed through its method
        } else if let Some(me) = me {
            pt.capability = me.r#for.clone();
        }
        // the roles of the method win over those of the step (ADR 0035 §2)
        let roles = me
            .and_then(|m| m.roles.as_ref())
            .or_else(|| s.and_then(|s| s.roles.as_ref()));
        if let Some(r) = roles {
            pt.responsible = r.responsible.clone();
            pt.accountable = r.accountable.clone();
        }
        out.points.push(pt);
    }
    for bl in &b.blocked {
        out.blocked.push(Blocked {
            id: bl.id.clone(),
            name: steps
                .get(bl.id.as_str())
                .map(|s| s.name.clone())
                .unwrap_or_default(),
            missing: bl.missing.clone(),
        });
    }
    out
}

/// conditions lists a set of conditions as "name" / "!name", sorted.
fn conditions(m: &HashMap<String, bool>) -> Vec<String> {
    let mut out: Vec<String> = m
        .iter()
        .map(|(k, v)| if *v { k.clone() } else { format!("!{k}") })
        .collect();
    out.sort();
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_gen::engine::v1::{BlockedStep, StartLaunch, StartingPoint};
    use goap_gen::registry::v1::Responsibilities;

    fn step(path: &str, name: &str, owner: &str) -> ViewStep {
        ViewStep {
            path: path.into(),
            name: name.into(),
            owner: owner.into(),
            ..Default::default()
        }
    }

    #[test]
    fn test_compose() {
        let view = MethodologyView {
            methodology: "m".into(),
            version: "1.0.0".into(),
            processes: vec![ViewActivity {
                name: "delivery".into(),
                kind: "process".into(),
                steps: vec![
                    ViewStep {
                        description: "Prepare".into(),
                        guidance: "Read first".into(),
                        exit: [("noted".to_string(), true), ("stale".to_string(), false)].into(),
                        roles: Some(Responsibilities {
                            responsible: "writer".into(),
                            accountable: "reviewer".into(),
                            ..Default::default()
                        }),
                        ..step("delivery/prepare", "prepare", "delivery")
                    },
                    ViewStep {
                        capability: "verification".into(),
                        roles: Some(Responsibilities {
                            responsible: "writer".into(),
                            ..Default::default()
                        }),
                        ..step("delivery/verify", "verify", "delivery")
                    },
                    step("delivery/approve", "approve", "delivery"),
                ],
                ..Default::default()
            }],
            methods: vec![ViewActivity {
                name: "peer_check".into(),
                kind: "method".into(),
                r#for: "verification".into(),
                roles: Some(Responsibilities {
                    responsible: "peer".into(),
                    ..Default::default()
                }),
                steps: vec![step("peer_check/read", "read", "peer_check")],
                ..Default::default()
            }],
            ..Default::default()
        };
        let b = ListStartingPointsResponse {
            methodology: "m".into(),
            version: "1.0.0".into(),
            goal: "ship".into(),
            blocked_count: 3,
            points: vec![
                StartingPoint {
                    id: "delivery/prepare".into(),
                    kind: KIND_STEP.into(),
                    why: vec!["framed".into()],
                    may_run: true,
                    launch: Some(StartLaunch {
                        methodology: "m".into(),
                        agent: "delivery".into(),
                        goal: "delivery/prepare".into(),
                        change_id: "C".into(),
                    }),
                    ..Default::default()
                },
                StartingPoint {
                    id: "delivery/verify".into(),
                    kind: KIND_METHOD.into(),
                    method: "peer_check".into(),
                    launch: Some(StartLaunch {
                        agent: "peer_check".into(),
                        goal: "peer_check".into(),
                        ..Default::default()
                    }),
                    ..Default::default()
                },
                StartingPoint {
                    id: "peer_check/read".into(),
                    kind: KIND_METHOD.into(),
                    parent: "delivery/verify".into(),
                    method: "peer_check".into(),
                    running: true,
                    ..Default::default()
                },
                StartingPoint {
                    id: "gone/step".into(),
                    kind: KIND_STEP.into(),
                    ..Default::default()
                },
            ],
            blocked: vec![BlockedStep {
                id: "delivery/approve".into(),
                missing: vec!["checked".into()],
            }],
            ..Default::default()
        };

        let got = compose(Some(&view), &b);
        assert!(
            got.methodology == "m"
                && got.version == "1.0.0"
                && got.goal == "ship"
                && got.blocked_count == 3
                && got.points.len() == 4,
            "{got:?}"
        );
        let p = &got.points[0];
        assert!(
            p.name == "prepare"
                && p.process == "delivery"
                && p.description == "Prepare"
                && p.guidance == "Read first"
                && p.responsible == "writer"
                && p.accountable == "reviewer"
                && p.produces == ["!stale", "noted"]
                && p.possible()
                && p.launch.change_id == "C",
            "step {p:?}"
        );
        // a step proposed through its method: the capability of the step, the roles of the method
        let p = &got.points[1];
        assert!(
            p.capability == "verification" && p.responsible == "peer" && p.name == "verify",
            "method {p:?}"
        );
        // a step of the method: the capability the method provides
        let p = &got.points[2];
        assert!(
            p.capability == "verification" && p.process == "peer_check" && !p.possible(),
            "step of a method {p:?}"
        );
        // a step the view does not hold keeps its binding
        let p = &got.points[3];
        assert!(p.id == "gone/step" && p.name.is_empty(), "unknown {p:?}");
        let bl = &got.blocked;
        assert!(
            bl.len() == 1 && bl[0].name == "approve" && bl[0].missing[0] == "checked",
            "blocked {bl:?}"
        );
        assert_eq!(
            got.find("delivery/verify").map(|p| p.method.as_str()),
            Some("peer_check"),
            "find"
        );
        // no view: the bindings only
        let bare = compose(None, &b);
        assert!(
            bare.points.len() == 4
                && bare.points[0].name.is_empty()
                && bare.points[0].launch.agent == "delivery",
            "bare {bare:?}"
        );
    }

    #[test]
    fn test_json_shape() {
        let p = Points {
            blocked: vec![Blocked {
                id: "x".into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        assert_eq!(
            serde_json::to_string(&p).unwrap(),
            r#"{"points":[],"blocked":[{"id":"x","missing":null}]}"#
        );
    }
}
