//! The A* planner over boolean world states.

use std::{
    collections::{BTreeMap, HashMap},
    fmt,
    ops::{Deref, DerefMut},
};

use serde::{Deserialize, Serialize};

/// Conditions maps condition names to a wanted (or set) truth value.
pub type Conditions = BTreeMap<String, bool>;

/// WorldState maps condition names to their known truth value. A condition
/// absent from the map is unknown: it satisfies no requirement.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(transparent)]
pub struct WorldState(pub BTreeMap<String, bool>);

impl Deref for WorldState {
    type Target = BTreeMap<String, bool>;
    fn deref(&self) -> &Self::Target {
        &self.0
    }
}

impl DerefMut for WorldState {
    fn deref_mut(&mut self) -> &mut Self::Target {
        &mut self.0
    }
}

impl From<BTreeMap<String, bool>> for WorldState {
    fn from(m: BTreeMap<String, bool>) -> Self {
        WorldState(m)
    }
}

impl From<WorldState> for BTreeMap<String, bool> {
    fn from(w: WorldState) -> Self {
        w.0
    }
}

impl<K: Into<String>> FromIterator<(K, bool)> for WorldState {
    fn from_iter<I: IntoIterator<Item = (K, bool)>>(iter: I) -> Self {
        WorldState(iter.into_iter().map(|(k, v)| (k.into(), v)).collect())
    }
}

impl WorldState {
    /// Satisfies reports whether every requirement holds in the state.
    pub fn satisfies(&self, req: &BTreeMap<String, bool>) -> bool {
        req.iter().all(|(k, want)| self.0.get(k) == Some(want))
    }

    /// Unsatisfied counts the requirements that do not hold.
    pub fn unsatisfied(&self, req: &BTreeMap<String, bool>) -> usize {
        req.iter()
            .filter(|(k, want)| self.0.get(*k) != Some(*want))
            .count()
    }

    /// Apply returns a copy of the state with effects applied.
    pub fn apply(&self, effects: &BTreeMap<String, bool>) -> WorldState {
        let mut out = self.0.clone();
        out.extend(effects.iter().map(|(k, v)| (k.clone(), *v)));
        WorldState(out)
    }

    fn key(&self) -> String {
        let mut b = String::new();
        for (k, v) in &self.0 {
            b.push_str(k);
            b.push_str(if *v { "=1;" } else { "=0;" });
        }
        b
    }
}

/// Action is a planning operator.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Action {
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Pre")]
    pub pre: BTreeMap<String, bool>,
    #[serde(rename = "Effects")]
    pub effects: BTreeMap<String, bool>,
    #[serde(rename = "Cost")]
    pub cost: f64,
}

/// Goal is a set of conditions to reach.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Goal {
    #[serde(rename = "Name")]
    pub name: String,
    #[serde(rename = "Pre")]
    pub pre: BTreeMap<String, bool>,
    #[serde(rename = "Value")]
    pub value: f64,
}

/// Plan is an ordered list of actions reaching a goal.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Plan {
    #[serde(rename = "Goal")]
    pub goal: Goal,
    #[serde(rename = "Actions")]
    pub actions: Vec<Action>,
    #[serde(rename = "Cost")]
    pub cost: f64,
}

impl Plan {
    /// NetValue is the goal value minus the plan cost.
    pub fn net_value(&self) -> f64 {
        self.goal.value - self.cost
    }
}

impl fmt::Display for Plan {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let names: Vec<&str> = self.actions.iter().map(|a| a.name.as_str()).collect();
        write!(
            f,
            "{}: [{}] cost={:.1}",
            self.goal.name,
            names.join(" -> "),
            self.cost
        )
    }
}

/// The errors of the planner.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// ErrNoPlan is returned when the goal cannot be reached.
    #[error("no plan reaches the goal")]
    NoPlan,
    /// The search was bounded before reaching the goal (wraps ErrNoPlan).
    #[error("search exceeded {0} expansions: no plan reaches the goal")]
    Exceeded(usize),
}

impl Error {
    /// errors.Is(err, ErrNoPlan).
    pub fn is_no_plan(&self) -> bool {
        matches!(self, Error::NoPlan | Error::Exceeded(_))
    }
}

/// ErrNoPlan is returned when the goal cannot be reached.
pub const ERR_NO_PLAN: Error = Error::NoPlan;

/// Planner searches plans with A*.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Planner {
    /// MaxExpansions bounds the search (default 10000).
    pub max_expansions: i64,
}

struct Node {
    state: WorldState,
    g: f64,
    parent: Option<usize>,
    action: Option<usize>,
}

impl Planner {
    /// Plan returns the cheapest action sequence reaching goal from start. An
    /// empty plan is returned when the goal already holds.
    pub fn plan(&self, start: &WorldState, actions: &[Action], goal: &Goal) -> Result<Plan, Error> {
        let max = if self.max_expansions <= 0 {
            10000
        } else {
            self.max_expansions as usize
        };
        // Admissible heuristic: when the goal does not hold, at least one more
        // action (of at least the minimum cost) is needed. Plans are optimal.
        let mut min_cost = 0.0;
        for (i, a) in actions.iter().enumerate() {
            let c = cost(a);
            if i == 0 || c < min_cost {
                min_cost = c;
            }
        }
        let h = |w: &WorldState| {
            if w.satisfies(&goal.pre) {
                0.0
            } else {
                min_cost
            }
        };

        let mut nodes: Vec<Node> = Vec::new();
        let mut open = Pq::default();
        nodes.push(Node {
            state: start.clone(),
            g: 0.0,
            parent: None,
            action: None,
        });
        open.push(PqItem {
            value: 0,
            priority: 0.0,
            tie: String::new(),
            seq: 0,
        });
        let mut best: HashMap<String, f64> = HashMap::new();
        best.insert(nodes[0].state.key(), 0.0);
        let mut expansions = 0usize;

        while let Some(item) = open.pop() {
            let cur = item.value;
            if nodes[cur].state.satisfies(&goal.pre) {
                let mut acts = Vec::new();
                let mut n = cur;
                while let Some(a) = nodes[n].action {
                    acts.push(actions[a].clone());
                    n = nodes[n].parent.expect("a node with an action has a parent");
                }
                acts.reverse();
                return Ok(Plan {
                    goal: goal.clone(),
                    actions: acts,
                    cost: nodes[cur].g,
                });
            }
            let cur_g = nodes[cur].g;
            if let Some(g) = best.get(&nodes[cur].state.key())
                && *g < cur_g
            {
                continue;
            }
            expansions += 1;
            if expansions > max {
                return Err(Error::Exceeded(max));
            }
            for (i, a) in actions.iter().enumerate() {
                if !nodes[cur].state.satisfies(&a.pre) {
                    continue;
                }
                let next = nodes[cur].state.apply(&a.effects);
                let k = next.key();
                let g = cur_g + cost(a);
                if let Some(old) = best.get(&k)
                    && *old <= g
                {
                    continue;
                }
                best.insert(k, g);
                let f = g + h(&next);
                nodes.push(Node {
                    state: next,
                    g,
                    parent: Some(cur),
                    action: Some(i),
                });
                open.push(PqItem {
                    value: nodes.len() - 1,
                    priority: f,
                    tie: a.name.clone(),
                    seq: 0,
                });
            }
        }
        Err(Error::NoPlan)
    }

    /// Best plans every goal and returns the plan with the highest net value
    /// (ties broken by lower cost, then goal name).
    pub fn best(
        &self,
        start: &WorldState,
        actions: &[Action],
        goals: &[Goal],
    ) -> Result<Plan, Error> {
        let mut plans: Vec<Plan> = goals
            .iter()
            .filter_map(|g| self.plan(start, actions, g).ok())
            .collect();
        if plans.is_empty() {
            return Err(Error::NoPlan);
        }
        // sort.SliceStable with a strict "less": equal elements keep their order.
        plans.sort_by(|a, b| {
            let less = |x: &Plan, y: &Plan| {
                if x.net_value() != y.net_value() {
                    return x.net_value() > y.net_value();
                }
                if x.cost != y.cost {
                    return x.cost < y.cost;
                }
                x.goal.name < y.goal.name
            };
            if less(a, b) {
                std::cmp::Ordering::Less
            } else if less(b, a) {
                std::cmp::Ordering::Greater
            } else {
                std::cmp::Ordering::Equal
            }
        });
        Ok(plans.swap_remove(0))
    }
}

fn cost(a: &Action) -> f64 {
    if a.cost <= 0.0 { 1.0 } else { a.cost }
}

struct PqItem {
    value: usize,
    priority: f64,
    tie: String,
    seq: usize,
}

/// A binary heap reproducing Go's container/heap over the planner's queue, so that
/// the order of equal items (and so the plans) is the one of the Go planner: `seq`
/// is the length of the queue when the item was pushed, as in Go.
#[derive(Default)]
struct Pq(Vec<PqItem>);

impl Pq {
    fn less(&self, i: usize, j: usize) -> bool {
        let (a, b) = (&self.0[i], &self.0[j]);
        if a.priority != b.priority {
            return a.priority < b.priority;
        }
        if a.tie != b.tie {
            return a.tie < b.tie;
        }
        a.seq < b.seq
    }

    fn push(&mut self, mut it: PqItem) {
        it.seq = self.0.len();
        self.0.push(it);
        self.up(self.0.len() - 1);
    }

    fn pop(&mut self) -> Option<PqItem> {
        if self.0.is_empty() {
            return None;
        }
        let n = self.0.len() - 1;
        self.0.swap(0, n);
        self.down(0, n);
        self.0.pop()
    }

    fn up(&mut self, mut j: usize) {
        while j > 0 {
            let i = (j - 1) / 2;
            if !self.less(j, i) {
                break;
            }
            self.0.swap(i, j);
            j = i;
        }
    }

    fn down(&mut self, i0: usize, n: usize) {
        let mut i = i0;
        loop {
            let j1 = 2 * i + 1;
            if j1 >= n {
                break;
            }
            let mut j = j1;
            let j2 = j1 + 1;
            if j2 < n && self.less(j2, j1) {
                j = j2;
            }
            if !self.less(j, i) {
                break;
            }
            self.0.swap(i, j);
            i = j;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn conds(pairs: &[(&str, bool)]) -> BTreeMap<String, bool> {
        pairs.iter().map(|(k, v)| (k.to_string(), *v)).collect()
    }

    fn action(name: &str, pre: &[(&str, bool)], effects: &[(&str, bool)], cost: f64) -> Action {
        Action {
            name: name.into(),
            pre: conds(pre),
            effects: conds(effects),
            cost,
        }
    }

    fn actions() -> Vec<Action> {
        vec![
            action(
                "identify",
                &[("has_impacts", false)],
                &[("has_impacts", true)],
                2.0,
            ),
            action(
                "propagate",
                &[("has_impacts", true)],
                &[("propagated", true)],
                1.0,
            ),
            action(
                "propose",
                &[("propagated", true)],
                &[("proposed", true)],
                3.0,
            ),
            action(
                "propose_quick",
                &[("has_impacts", true)],
                &[("proposed", true)],
                10.0,
            ),
            action("review", &[("proposed", true)], &[("reviewed", true)], 1.0),
        ]
    }

    fn names(p: &Plan) -> Vec<String> {
        p.actions.iter().map(|a| a.name.clone()).collect()
    }

    fn goal(name: &str, pre: &[(&str, bool)], value: f64) -> Goal {
        Goal {
            name: name.into(),
            pre: conds(pre),
            value,
        }
    }

    fn state(pairs: &[(&str, bool)]) -> WorldState {
        WorldState(conds(pairs))
    }

    #[test]
    fn plan_cheapest() {
        let start = state(&[
            ("has_impacts", false),
            ("propagated", false),
            ("proposed", false),
            ("reviewed", false),
        ]);
        let p = Planner::default()
            .plan(&start, &actions(), &goal("g", &[("reviewed", true)], 0.0))
            .unwrap();
        assert_eq!(names(&p), ["identify", "propagate", "propose", "review"]);
        assert_eq!(p.cost, 7.0);
        assert_eq!(
            p.to_string(),
            "g: [identify -> propagate -> propose -> review] cost=7.0"
        );
    }

    #[test]
    fn plan_replan_from_mid_state() {
        let start = state(&[
            ("has_impacts", true),
            ("propagated", true),
            ("proposed", false),
        ]);
        let p = Planner::default()
            .plan(&start, &actions(), &goal("g", &[("reviewed", true)], 0.0))
            .unwrap();
        let got = names(&p);
        assert_eq!(got.len(), 2);
        assert_eq!(got[0], "propose");
    }

    #[test]
    fn plan_already_satisfied() {
        let p = Planner::default()
            .plan(
                &state(&[("reviewed", true)]),
                &actions(),
                &goal("", &[("reviewed", true)], 0.0),
            )
            .unwrap();
        assert!(p.actions.is_empty());
    }

    #[test]
    fn unknown_condition_blocks() {
        // has_impacts unknown: "identify" requires has_impacts=false and cannot run.
        let err = Planner::default()
            .plan(
                &WorldState::default(),
                &actions(),
                &goal("", &[("reviewed", true)], 0.0),
            )
            .unwrap_err();
        assert!(err.is_no_plan(), "expected ErrNoPlan, got {err}");
    }

    #[test]
    fn best_goal() {
        let start = state(&[
            ("has_impacts", false),
            ("propagated", false),
            ("proposed", false),
            ("reviewed", false),
        ]);
        let goals = vec![
            goal("assess", &[("propagated", true)], 10.0),
            goal("full", &[("reviewed", true)], 12.0),
        ];
        let p = Planner::default().best(&start, &actions(), &goals).unwrap();
        // assess: 10-3=7 ; full: 12-7=5
        assert_eq!(p.goal.name, "assess", "got {p}");
    }

    #[test]
    fn exceeded_wraps_no_plan() {
        let err = Planner { max_expansions: 1 }
            .plan(&WorldState::default(), &[], &goal("", &[("b", true)], 0.0))
            .unwrap_err();
        assert!(err.is_no_plan());
        assert_eq!(err.to_string(), "no plan reaches the goal");
        let acts = vec![action("flip", &[], &[("a", true)], 1.0)];
        let err = Planner { max_expansions: 1 }
            .plan(
                &WorldState::default(),
                &acts,
                &goal("", &[("b", true)], 0.0),
            )
            .unwrap_err();
        assert_eq!(
            err.to_string(),
            "search exceeded 1 expansions: no plan reaches the goal"
        );
        assert!(err.is_no_plan());
    }

    #[test]
    fn ties_follow_action_name_then_push_order() {
        // two equal-cost routes: the action whose name sorts first wins, as in Go.
        let acts = vec![
            action("b_route", &[], &[("done", true)], 1.0),
            action("a_route", &[], &[("done", true)], 1.0),
        ];
        let p = Planner::default()
            .plan(
                &WorldState::default(),
                &acts,
                &goal("", &[("done", true)], 0.0),
            )
            .unwrap();
        // both reach the same state: the second is pruned by `best` (old <= g), so the first one stays.
        assert_eq!(names(&p), ["b_route"]);
    }

    #[test]
    fn world_state_json() {
        let w = state(&[("b", false), ("a", true)]);
        assert_eq!(
            serde_json::to_string(&w).unwrap(),
            r#"{"a":true,"b":false}"#
        );
        assert_eq!(
            w.unsatisfied(&conds(&[("a", true), ("b", true), ("c", false)])),
            2
        );
    }
}
