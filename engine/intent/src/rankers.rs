//! The rankers: lexical (deterministic) and by a language model.

use std::{
    collections::{BTreeSet, HashSet},
    sync::{Arc, LazyLock},
};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_llm::{CallMeta, Client, Message, Request, SOURCE_INTENT, decode_json, with_meta};
use serde::Deserialize;
use unicode_normalization::UnicodeNormalization;
use unicode_properties::{GeneralCategory, GeneralCategoryGroup, UnicodeGeneralCategory};

use crate::intent::{Candidate, Clarifier, Error, GoalInfo, Ranker, Turn};

/// Lexical ranks goals by token overlap between the user turns and the goal
/// name, description and examples. Deterministic; used in dev and tests.
#[derive(Debug, Clone, Copy, Default)]
pub struct Lexical;

#[async_trait]
impl Ranker for Lexical {
    async fn rank(
        &self,
        _ctx: &Ctx,
        turns: &[Turn],
        goals: &[GoalInfo],
    ) -> Result<Vec<Candidate>, Error> {
        Ok(lexical_rank(turns, goals))
    }
}

fn lexical_rank(turns: &[Turn], goals: &[GoalInfo]) -> Vec<Candidate> {
    let mut text = String::new();
    for t in turns {
        if t.role == "user" {
            text.push(' ');
            text.push_str(&t.text);
        }
    }
    let utter = normalize(&text);
    let words = tokens(&utter);
    let mut scores = vec![0.0f64; goals.len()];
    for (i, g) in goals.iter().enumerate() {
        // the technical goal name is left out: identifiers like "prepare_change"
        // would match everyday words (an exact name is handled by the resolver)
        let vocab = tokens(&normalize(&format!(
            "{} {}",
            g.description,
            g.examples.join(" ")
        )));
        for w in &words {
            if vocab.contains(w) {
                scores[i] += 1.0;
            }
        }
        for ex in &g.examples {
            let e = normalize(ex);
            if !e.is_empty() && utter.contains(&e) {
                scores[i] += 3.0;
            }
        }
    }
    // confidence is relative to the best competitor, so that it does not
    // dilute when many goals match weakly
    goals
        .iter()
        .enumerate()
        .map(|(i, g)| {
            let mut best = 0.0;
            for (j, s) in scores.iter().enumerate() {
                if j != i && *s > best {
                    best = *s;
                }
            }
            Candidate {
                goal: g.name.clone(),
                confidence: scores[i] / (scores[i] + best + 1.0),
                reason: format!("score {:.0}", scores[i]),
                ..Default::default()
            }
        })
        .collect()
}

static STOPWORDS: LazyLock<HashSet<&'static str>> = LazyLock::new(|| {
    [
        "les", "des", "une", "est", "que", "qui", "pour", "dans", "sur", "avec", "the", "and",
        "for", "sans", "par", "pas", "quel",
    ]
    .into_iter()
    .collect()
});

fn tokens(s: &str) -> BTreeSet<String> {
    s.split(|r: char| {
        r.general_category_group() != GeneralCategoryGroup::Letter
            && r.general_category() != GeneralCategory::DecimalNumber
    })
    .filter(|w| w.len() >= 3 && !STOPWORDS.contains(w))
    .map(str::to_string)
    .collect()
}

fn normalize(s: &str) -> String {
    let lower: String = s.chars().flat_map(char::to_lowercase).collect();
    let r: String = lower
        .nfd()
        .filter(|c| c.general_category() != GeneralCategory::NonspacingMark)
        .nfc()
        .collect();
    let r = r.replace(['\'', '\u{2019}'], " ");
    r.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// LLMRanker ranks goals with a language model.
#[derive(Clone)]
pub struct LLMRanker {
    pub client: Arc<dyn Client>,
    pub model: String,
}

const RANK_SYSTEM: &str = r#"You classify a user request against the goals of an enterprise methodology.
Answer with a single JSON object: {"candidates":[{"goal":"<name>","confidence":<0..1>,"reason":"..."}],"question":"<clarification question in the user's language, or empty>"}.
Confidences express how sure you are that the user wants this goal; they need not sum to 1. List every goal."#;

#[derive(Deserialize, Default)]
#[serde(default)]
struct RankAnswer {
    #[serde(deserialize_with = "null_vec")]
    candidates: Vec<Candidate>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct ClarifyAnswer {
    question: String,
}

fn null_vec<'de, D: serde::Deserializer<'de>>(d: D) -> Result<Vec<Candidate>, D::Error> {
    Ok(Option::<Vec<Candidate>>::deserialize(d)?.unwrap_or_default())
}

#[async_trait]
impl Ranker for LLMRanker {
    async fn rank(
        &self,
        ctx: &Ctx,
        turns: &[Turn],
        goals: &[GoalInfo],
    ) -> Result<Vec<Candidate>, Error> {
        let out: RankAnswer = self.ask(ctx, turns, goals).await?;
        let known: HashSet<&str> = goals.iter().map(|g| g.name.as_str()).collect();
        Ok(out
            .candidates
            .into_iter()
            .filter(|c| known.contains(c.goal.as_str()))
            .collect())
    }

    fn as_clarifier(&self) -> Option<&dyn Clarifier> {
        Some(self)
    }
}

#[async_trait]
impl Clarifier for LLMRanker {
    async fn clarify(
        &self,
        ctx: &Ctx,
        turns: &[Turn],
        _candidates: &[Candidate],
        goals: &[GoalInfo],
    ) -> Result<String, Error> {
        let out: ClarifyAnswer = self.ask(ctx, turns, goals).await?;
        Ok(out.question)
    }
}

impl LLMRanker {
    async fn ask<T: serde::de::DeserializeOwned>(
        &self,
        ctx: &Ctx,
        turns: &[Turn],
        goals: &[GoalInfo],
    ) -> Result<T, Error> {
        let g = go_marshal_indent(&goals);
        let mut dialog = String::new();
        for t in turns {
            dialog.push_str(&format!("{}: {}\n", t.role, t.text));
        }
        let model = if self.model.is_empty() {
            "fast".to_string()
        } else {
            self.model.clone()
        };
        let ctx = with_meta(
            ctx,
            CallMeta {
                source: SOURCE_INTENT.into(),
                ..Default::default()
            },
        );
        let resp = self
            .client
            .complete(
                &ctx,
                Request {
                    model,
                    system: RANK_SYSTEM.into(),
                    json: true,
                    max_tokens: 2048,
                    messages: vec![Message {
                        role: "user".into(),
                        content: format!("Goals:\n{g}\n\nDialogue:\n{dialog}"),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await?;
        Ok(decode_json(&resp.text)?)
    }
}

/// `json.MarshalIndent(v, "", "  ")`: the indented JSON text with `<`, `>`, `&`, U+2028 and U+2029 escaped as Go does.
fn go_marshal_indent<T: serde::Serialize + ?Sized>(v: &T) -> String {
    let s = serde_json::to_string_pretty(v).unwrap_or_default();
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_llm::{ClientFunc, Response, meta_from};
    use parking_lot::Mutex;

    // The ranker declares itself to the ledger of the gateway (ADR 0089).
    #[tokio::test]
    async fn llm_ranker_stamps_its_source() {
        let got = Arc::new(Mutex::new(CallMeta::default()));
        let seen = got.clone();
        let r = LLMRanker {
            client: ClientFunc::shared(move |ctx: Ctx, _req: Request| {
                let seen = seen.clone();
                async move {
                    *seen.lock() = meta_from(&ctx);
                    Ok(Response {
                        text: r#"{"candidates":[]}"#.into(),
                        ..Default::default()
                    })
                }
            }),
            model: String::new(),
        };
        r.rank(&Ctx::background(), &[Turn::new("user", "x")], &[])
            .await
            .unwrap();
        assert_eq!(got.lock().source, SOURCE_INTENT, "{:?}", got.lock());
    }

    #[test]
    fn normalize_folds_accents_and_quotes() {
        assert_eq!(
            normalize("  L'Évaluation   d’impact "),
            "l evaluation d impact"
        );
        let t = tokens(&normalize("impact du changement, été 2024 ok"));
        assert!(
            t.contains("impact") && t.contains("ete") && t.contains("2024") && !t.contains("ok")
        );
    }
}
