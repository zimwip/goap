//! The intent loop: rank, then select a goal or ask a clarification question.

use std::{collections::HashMap, sync::Arc};

use async_trait::async_trait;
use goap_ctx::Ctx;
use serde::{Deserialize, Deserializer, Serialize, Serializer};

/// GoalInfo describes a goal to the ranker.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct GoalInfo {
    pub name: String,
    pub description: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub examples: Vec<String>,
}

/// Turn is one exchange of the intent dialogue.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Turn {
    /// user | assistant
    pub role: String,
    pub text: String,
}

impl Turn {
    /// A turn of `role` saying `text`.
    pub fn new(role: impl Into<String>, text: impl Into<String>) -> Turn {
        Turn {
            role: role.into(),
            text: text.into(),
        }
    }
}

/// Candidate is a ranked goal.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Candidate {
    pub goal: String,
    pub confidence: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    /// Agent and Methodology are filled by the engine for agent identification.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub agent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
}

/// The errors of the intent loop.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// Resolve was given no goal.
    #[error("no goal to resolve")]
    NoGoal,
    /// A model call of a ranker failed.
    #[error(transparent)]
    Llm(#[from] goap_llm::Error),
    /// Any other error of a ranker.
    #[error(transparent)]
    Other(Box<dyn std::error::Error + Send + Sync>),
}

/// Ranker ranks goals for a dialogue. Candidates are returned by decreasing
/// confidence.
#[async_trait]
pub trait Ranker: Send + Sync {
    async fn rank(
        &self,
        ctx: &Ctx,
        turns: &[Turn],
        goals: &[GoalInfo],
    ) -> Result<Vec<Candidate>, Error>;

    /// The ranker as a Clarifier, when it is one (Go's type assertion `r.Ranker.(Clarifier)`).
    fn as_clarifier(&self) -> Option<&dyn Clarifier> {
        None
    }
}

/// Clarifier optionally generates a clarification question.
#[async_trait]
pub trait Clarifier: Send + Sync {
    async fn clarify(
        &self,
        ctx: &Ctx,
        turns: &[Turn],
        candidates: &[Candidate],
        goals: &[GoalInfo],
    ) -> Result<String, Error>;
}

/// Session is the state of an intent dialogue.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Session {
    /// Encoded `null` when empty, as Go's nil slice.
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub turns: Vec<Turn>,
    /// Offered lists the goals proposed in the last clarification question, so
    /// that an answer like "2" can select one.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub offered: Vec<String>,
}

/// Resolution is the outcome of one resolution round.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Resolution {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub question: String,
    /// Encoded `null` when empty, as Go's nil slice.
    #[serde(serialize_with = "nil_vec", deserialize_with = "null_default")]
    pub candidates: Vec<Candidate>,
}

impl Resolution {
    /// Resolved reports whether a goal was selected.
    pub fn resolved(&self) -> bool {
        !self.goal.is_empty()
    }
}

/// Resolver drives the intent loop.
#[derive(Clone)]
pub struct Resolver {
    pub ranker: Arc<dyn Ranker>,
    /// Threshold is the minimum confidence of the selected goal (default 0.6).
    pub threshold: f64,
    /// Margin is the minimum gap with the second candidate (default 0.15).
    pub margin: f64,
    /// MaxTurns bounds clarification rounds; afterwards the best candidate is
    /// selected if its confidence is positive (default 3).
    pub max_turns: i64,
}

impl Resolver {
    /// A resolver over `ranker` with the default threshold, margin and turns.
    pub fn new(ranker: Arc<dyn Ranker>) -> Resolver {
        Resolver {
            ranker,
            threshold: 0.0,
            margin: 0.0,
            max_turns: 0,
        }
    }

    /// Resolve runs one round: rank, then select or ask. The session is updated
    /// with the assistant question when one is asked.
    pub async fn resolve(
        &self,
        ctx: &Ctx,
        s: &mut Session,
        goals: &[GoalInfo],
    ) -> Result<Resolution, Error> {
        if goals.is_empty() {
            return Err(Error::NoGoal);
        }
        let threshold = if self.threshold == 0.0 {
            0.6
        } else {
            self.threshold
        };
        let margin = if self.margin == 0.0 {
            0.15
        } else {
            self.margin
        };
        let max_turns = if self.max_turns == 0 {
            3
        } else {
            self.max_turns
        };
        if goals.len() == 1 {
            return Ok(Resolution {
                goal: goals[0].name.clone(),
                question: String::new(),
                candidates: vec![Candidate {
                    goal: goals[0].name.clone(),
                    confidence: 1.0,
                    reason: "single goal".into(),
                    ..Default::default()
                }],
            });
        }
        let g = pick_offered(s, goals);
        if !g.is_empty() {
            s.offered = Vec::new();
            return Ok(Resolution {
                goal: g.clone(),
                question: String::new(),
                candidates: vec![Candidate {
                    goal: g,
                    confidence: 1.0,
                    reason: "explicit choice".into(),
                    ..Default::default()
                }],
            });
        }
        let mut cands = self.ranker.rank(ctx, &s.turns, goals).await?;
        // sort.SliceStable with "less" = greater confidence
        cands.sort_by(|a, b| {
            if a.confidence > b.confidence {
                std::cmp::Ordering::Less
            } else if b.confidence > a.confidence {
                std::cmp::Ordering::Greater
            } else {
                std::cmp::Ordering::Equal
            }
        });
        let mut res = Resolution {
            candidates: cands,
            ..Default::default()
        };
        if let Some(top) = res.candidates.first() {
            let second = res.candidates.get(1).map(|c| c.confidence).unwrap_or(0.0);
            if top.confidence >= threshold && top.confidence - second >= margin {
                res.goal = top.goal.clone();
                s.offered = Vec::new();
                return Ok(res);
            }
            if user_turns(s) > max_turns && top.confidence > 0.0 {
                res.goal = top.goal.clone();
                s.offered = Vec::new();
                return Ok(res);
            }
        }
        let (question, offered) = self.question(ctx, s, &res.candidates, goals).await;
        res.question = question;
        s.offered = offered;
        s.turns.push(Turn::new("assistant", res.question.clone()));
        Ok(res)
    }

    async fn question(
        &self,
        ctx: &Ctx,
        s: &Session,
        cands: &[Candidate],
        goals: &[GoalInfo],
    ) -> (String, Vec<String>) {
        let mut offered: Vec<String> = Vec::with_capacity(3);
        for c in cands {
            if offered.len() == 3 {
                break;
            }
            if c.confidence > 0.0 {
                offered.push(c.goal.clone());
            }
        }
        if offered.len() < 2 {
            offered.clear();
            offered.extend(goals.iter().map(|g| g.name.clone()));
        }
        if let Some(c) = self.ranker.as_clarifier()
            && let Ok(q) = c.clarify(ctx, &s.turns, cands, goals).await
            && !q.is_empty()
        {
            return (q, offered);
        }
        let desc: HashMap<&str, &str> = goals
            .iter()
            .map(|g| (g.name.as_str(), g.description.as_str()))
            .collect();
        let mut b = String::from("I'm not sure of the goal. Would you like to:\n");
        for (i, g) in offered.iter().enumerate() {
            b.push_str(&format!(
                "{}) {}\n",
                i + 1,
                desc.get(g.as_str()).copied().unwrap_or("")
            ));
        }
        b.push_str("Reply with the number or rephrase.");
        (b, offered)
    }
}

fn lower(s: &str) -> String {
    s.chars().flat_map(char::to_lowercase).collect()
}

fn pick_offered(s: &Session, goals: &[GoalInfo]) -> String {
    let Some(last) = s.turns.last() else {
        return String::new();
    };
    if last.role != "user" {
        return String::new();
    }
    let answer = last.text.trim_end_matches(['.', ')']).trim();
    if let Ok(n) = answer.parse::<i64>()
        && n >= 1
        && (n as usize) <= s.offered.len()
    {
        return s.offered[n as usize - 1].clone();
    }
    let la = lower(answer);
    for g in goals {
        // goal names may be qualified ("methodology/agent/goal"): accept any suffix
        let ln = lower(&g.name);
        if ln == la || ln.ends_with(&format!("/{la}")) {
            return g.name.clone();
        }
    }
    String::new()
}

fn user_turns(s: &Session) -> i64 {
    s.turns.iter().filter(|t| t.role == "user").count() as i64
}

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

fn nil_vec<S: Serializer, T: Serialize>(v: &[T], s: S) -> Result<S::Ok, S::Error> {
    if v.is_empty() {
        s.serialize_none()
    } else {
        v.serialize(s)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Lexical;

    fn goals() -> Vec<GoalInfo> {
        vec![
            GoalInfo {
                name: "assess_impact".into(),
                description: "Assess the impact of a change and produce a report".into(),
                examples: vec!["what is the impact".into(), "what does this break".into()],
            },
            GoalInfo {
                name: "prepare_change".into(),
                description: "Prepare the update of the repository and get it validated".into(),
                examples: vec!["update the requirements".into()],
            },
        ]
    }

    #[tokio::test]
    async fn resolve_direct() {
        let mut s = Session {
            turns: vec![Turn::new(
                "user",
                "The PSP is changing its API: what does this break?",
            )],
            ..Default::default()
        };
        let res = Resolver::new(Arc::new(Lexical))
            .resolve(&Ctx::background(), &mut s, &goals())
            .await
            .unwrap();
        assert_eq!(res.goal, "assess_impact", "got {res:?}");
    }

    #[tokio::test]
    async fn resolve_clarify_then_choose() {
        let mut s = Session {
            turns: vec![Turn::new("user", "The payment provider is changing")],
            ..Default::default()
        };
        let r = Resolver::new(Arc::new(Lexical));
        let ctx = Ctx::background();
        let res = r.resolve(&ctx, &mut s, &goals()).await.unwrap();
        assert!(
            !res.resolved() && res.question.contains("1)"),
            "expected a clarification, got {res:?}"
        );
        s.turns.push(Turn::new("user", "2"));
        let res = r.resolve(&ctx, &mut s, &goals()).await.unwrap();
        assert_eq!(
            res.goal, "prepare_change",
            "expected prepare_change, got {res:?}"
        );
    }

    #[tokio::test]
    async fn resolve_gives_up_after_max_turns() {
        let mut s = Session::default();
        let mut r = Resolver::new(Arc::new(Lexical));
        r.max_turns = 1;
        let ctx = Ctx::background();
        for txt in ["bonjour", "impact changement rapport modification"] {
            s.turns.push(Turn::new("user", txt));
            if let Ok(res) = r.resolve(&ctx, &mut s, &goals()).await
                && res.resolved()
            {
                return;
            }
        }
        panic!("expected a best-effort resolution after max turns");
    }

    #[test]
    fn session_json_is_go_shaped() {
        assert_eq!(
            serde_json::to_string(&Session::default()).unwrap(),
            r#"{"turns":null}"#
        );
        let s: Session = serde_json::from_str(r#"{"turns":null,"offered":null}"#).unwrap();
        assert_eq!(s, Session::default());
        let r = Resolution::default();
        assert_eq!(serde_json::to_string(&r).unwrap(), r#"{"candidates":null}"#);
    }
}
