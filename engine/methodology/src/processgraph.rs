//! A process as a graph (ADR 0036 §4, `processgraph.go`).

use std::collections::{BTreeMap, BTreeSet, HashSet};

use serde::{Deserialize, Serialize};

use crate::method::{METHOD_CAPABILITY, Method, Responsibilities};
use crate::methodology::Compiled;
use crate::process::Reference;
use crate::review::Review;
use crate::util::Conds;
use crate::view::StepView;

/// ProcessGraph is a process as a graph: its steps, the edges the conditions draw between them, and the methods
/// behind the steps that name a capability, with their agents and the agents' actions.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProcessGraph {
    pub process: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub steps: Vec<GraphStep>,
    pub edges: Vec<GraphEdge>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub methods: Vec<GraphMeth>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<Reference>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub agents: Vec<GraphAgent>,
}

/// GraphStep is a step of the process graph: a leaf (it runs something) or a phase.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct GraphStep {
    pub path: String,
    pub name: String,
    pub description: String,
    pub parent: String,
    pub depth: i64,
    pub leaf: bool,
    /// Method: steps, action, agent, process, method or manual; Target what it runs.
    pub method: String,
    pub target: String,
    pub entry: Conds,
    pub exit: Conds,
    pub roles: Option<Responsibilities>,
    pub guidance: String,
    pub references: Vec<Reference>,
    pub process: String,
    pub capability: String,
    pub foreach: String,
    pub group_by: String,
    pub review: Option<Review>,
}

/// GraphEdge says that the exit criteria of From meet the entry of To, on the given conditions.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct GraphEdge {
    pub from: String,
    pub to: String,
    pub conditions: Vec<String>,
}

/// GraphMeth is a method of a capability a step names, with the agent that acts.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct GraphMeth {
    #[serde(flatten)]
    pub method: Method,
    #[serde(rename = "AgentGoal", default)]
    pub agent_goal: String,
}

impl std::ops::Deref for GraphMeth {
    type Target = Method;
    fn deref(&self) -> &Method {
        &self.method
    }
}

/// GraphAgent is an agent an agent step or a method names, with its actions.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct GraphAgent {
    pub name: String,
    pub description: String,
    pub planner: String,
    pub goals: Vec<String>,
    pub actions: Vec<GraphAction>,
}

/// GraphAction is an action an agent may plan with.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct GraphAction {
    pub name: String,
    pub kind: String,
    pub description: String,
    pub pre: Conds,
    pub effects: Conds,
}

impl Compiled {
    /// ProcessGraph is the flow of a process, or of a method composing its own steps or actions (None when the
    /// methodology has neither): a projection of the compiled view (ADR 0126 step 3).
    pub fn process_graph(&self, name: &str) -> Option<ProcessGraph> {
        let v = self.view();
        let a = match v.process(name) {
            Some(a) => a,
            None => {
                let a = v.method(name)?;
                if a.steps.is_empty() && a.actions.is_empty() {
                    return None;
                }
                a
            }
        };
        let mut g = ProcessGraph {
            process: a.name.clone(),
            description: a.description.clone(),
            references: a.references.clone(),
            ..ProcessGraph::default()
        };
        let mut capabilities: HashSet<String> = HashSet::new();
        fn walk(
            g: &mut ProcessGraph,
            caps: &mut HashSet<String>,
            steps: &[StepView],
            parent: &str,
            depth: i64,
        ) {
            for s in steps {
                g.steps.push(GraphStep {
                    path: s.path.clone(),
                    name: s.name.clone(),
                    description: s.description.clone(),
                    parent: parent.to_string(),
                    depth,
                    leaf: s.steps.is_empty(),
                    method: s.method.clone(),
                    target: s.target.clone(),
                    entry: s.entry.clone(),
                    exit: s.exit.clone(),
                    roles: s.roles.clone(),
                    guidance: s.guidance.clone(),
                    references: s.references.clone(),
                    process: s.process.clone(),
                    capability: s.capability.clone(),
                    foreach: s.foreach.clone(),
                    group_by: s.group_by.clone(),
                    review: s.review.clone(),
                });
                if s.method == METHOD_CAPABILITY {
                    caps.insert(s.capability.clone());
                }
                walk(g, caps, &s.steps, &s.path, depth + 1);
            }
        }
        walk(&mut g, &mut capabilities, &a.steps, "", 0);
        g.edges = condition_edges(&g.steps);
        let mut agents: BTreeSet<String> = BTreeSet::new();
        for me in &self.definition.methods {
            if capabilities.contains(&me.r#for) {
                g.methods.push(GraphMeth {
                    method: me.clone(),
                    agent_goal: self.method_goal(&me.name),
                });
                agents.insert(me.actor_agent().to_string());
            }
        }
        for n in &agents {
            let Some(ag) = self.agent(n) else {
                continue;
            };
            let mut ga = GraphAgent {
                name: ag.name.clone(),
                description: ag.description.clone(),
                planner: ag.planner.clone(),
                goals: ag.goals.clone(),
                actions: Vec::new(),
            };
            for a in self.agent_actions(ag) {
                let full = self.action(&a.name).cloned().unwrap_or_default();
                ga.actions.push(GraphAction {
                    name: a.name.clone(),
                    kind: full.kind,
                    description: full.description,
                    pre: a.pre.clone(),
                    effects: a.effects.clone(),
                });
            }
            g.agents.push(ga);
        }
        Some(g)
    }
}

/// conditionEdges draws an edge from a leaf step to another when its exit criteria meet what the other needs, then
/// drops the edges implied by a longer path (transitive reduction).
pub(crate) fn condition_edges(steps: &[GraphStep]) -> Vec<GraphEdge> {
    let leaves: Vec<&GraphStep> = steps.iter().filter(|s| s.leaf).collect();
    let mut conds: BTreeMap<(String, String), Vec<String>> = BTreeMap::new();
    let mut next: BTreeMap<String, Vec<String>> = BTreeMap::new();
    for a in &leaves {
        for b in &leaves {
            if a.path == b.path {
                continue;
            }
            let mut on = Vec::new();
            for (k, v) in &b.entry {
                if a.exit.get(k) != Some(v) {
                    continue;
                }
                if a.entry.get(k) == Some(v) {
                    continue; // a needs it already: it keeps it, it does not produce it
                }
                if b.exit.get(k).is_some_and(|w| w != v) {
                    continue; // b's own "not done yet" guard
                }
                on.push(k.clone());
            }
            if !on.is_empty() {
                conds.insert((a.path.clone(), b.path.clone()), on);
                next.entry(a.path.clone()).or_default().push(b.path.clone());
            }
        }
    }
    // reachable without the direct edge
    let reach = |from: &str, to: &str| -> bool {
        let mut seen: HashSet<String> = HashSet::new();
        let mut stack: Vec<String> = next
            .get(from)
            .into_iter()
            .flatten()
            .filter(|n| *n != to)
            .cloned()
            .collect();
        while let Some(n) = stack.pop() {
            if n == to {
                return true;
            }
            if !seen.insert(n.clone()) {
                continue;
            }
            stack.extend(next.get(&n).into_iter().flatten().cloned());
        }
        false
    };
    let mut out = Vec::new();
    for a in &leaves {
        for b in next.get(&a.path).into_iter().flatten() {
            if !reach(&a.path, b) {
                out.push(GraphEdge {
                    from: a.path.clone(),
                    to: b.clone(),
                    conditions: conds[&(a.path.clone(), b.clone())].clone(),
                });
            }
        }
    }
    out
}
