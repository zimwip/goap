//! Global behaviours of the LLM calls (ADR 0093).

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use sha2::{Digest, Sha256};

use crate::{Error, Snapshot, from_props, go_quote, split_target, to_props, valid_name};

/// NodeTypeBehavior is the node type of a global behaviour of the LLM calls (ADR 0093).
pub const NODE_TYPE_BEHAVIOR: &str = "platform@LlmBehavior";

// Positions of a behaviour in the system text of a call.
pub const POSITION_PREPEND: &str = "prepend";
pub const POSITION_APPEND: &str = "append";

// Caps of ADR 0093 (bytes).

/// MaxInstruction caps the instruction of one behaviour.
pub const MAX_INSTRUCTION: usize = 4 << 10;
/// MaxBehaviorBytes caps the text all the behaviours add to one call: the ones that do not fit are dropped (the lowest
/// priority last) and the call records them as skipped.
pub const MAX_BEHAVIOR_BYTES: usize = 8 << 10;

/// KindComplete is the only kind of call a behaviour may apply to: an embedding has no instructions.
pub const KIND_COMPLETE: &str = "complete";

/// JSONNotice wraps the instruction of a behaviour applied to a call that requires structured output, so that the style
/// cannot change the format.
pub const JSON_NOTICE: &str = "These style rules apply to free-text fields only; the output format required by the other instructions is unchanged.\n";

/// Sources a behaviour may be scoped to (the callers of llm.CallMeta the ledger knows).
pub fn sources() -> Vec<&'static str> {
    vec![
        goap_llm::SOURCE_ENGINE,
        goap_llm::SOURCE_ASSISTANT,
        goap_llm::SOURCE_HELPER,
        goap_llm::SOURCE_INDEXER,
        goap_llm::SOURCE_INTENT,
        goap_llm::SOURCE_OTHER,
    ]
}

fn is_false(b: &bool) -> bool {
    !*b
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// Behavior is an instruction the gateway adds to the system text of the calls that match its scope, so that a platform
/// wide style (terse answers, a language, a tone) needs no change in any prompt. Every selector is optional and the
/// selectors are ANDed; an empty one matches everything.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Behavior {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// Instruction is the text added to the instructions of the model (at most MAX_INSTRUCTION bytes).
    pub instruction: String,
    pub enabled: bool,
    /// Position is prepend or append (default append) relative to the system text of the request.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub position: String,
    /// Order sorts the behaviours, lower first, then by name.
    #[serde(skip_serializing_if = "is_zero")]
    pub order: i64,
    /// Aliases limits it to the calls requesting one of these aliases ("default" for a call naming none).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub aliases: Vec<String>,
    /// Models limits it to the calls served by one of these provider/model.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub models: Vec<String>,
    /// Sources limits it to the calls of these callers (llm.CallMeta.Source).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<String>,
    /// Kinds limits it to these kinds of call; only complete exists (a behaviour never applies to an embedding).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub kinds: Vec<String>,
    /// AppliesToJSON lets it apply to the calls that require a JSON answer (default: it does not, so it cannot break a protocol).
    #[serde(rename = "appliesToJSON", skip_serializing_if = "is_false")]
    pub applies_to_json: bool,
}

/// BehaviorKey is the key of the node of a behaviour.
pub fn behavior_key(name: &str) -> String {
    format!("LLB:{name}")
}

impl Behavior {
    /// Validate checks a behaviour.
    pub fn validate(&self) -> Result<(), Error> {
        let name = &self.name;
        if !valid_name(name) {
            return Err(Error::invalid(
                "behavior name must be 1-40 characters of a-z, 0-9, - or _",
            ));
        }
        if self.instruction.trim().is_empty() {
            return Err(Error::invalid(format!(
                "behavior {name}: instruction required"
            )));
        }
        if self.instruction.len() > MAX_INSTRUCTION {
            return Err(Error::invalid(format!(
                "behavior {name}: instruction is {} bytes, at most {MAX_INSTRUCTION}",
                self.instruction.len()
            )));
        }
        match self.position.as_str() {
            "" | POSITION_PREPEND | POSITION_APPEND => {}
            _ => {
                return Err(Error::invalid(format!(
                    "behavior {name}: position must be prepend or append"
                )));
            }
        }
        for a in &self.aliases {
            if !valid_name(a) {
                return Err(Error::invalid(format!(
                    "behavior {name}: alias {} is not a valid alias name",
                    go_quote(a)
                )));
            }
        }
        for m in &self.models {
            if split_target(m).is_none() {
                return Err(Error::invalid(format!(
                    "behavior {name}: model {} must be provider/model",
                    go_quote(m)
                )));
            }
        }
        for s in &self.sources {
            if !sources().contains(&s.as_str()) {
                return Err(Error::invalid(format!(
                    "behavior {name}: source {} must be one of {}",
                    go_quote(s),
                    sources().join(", ")
                )));
            }
        }
        for k in &self.kinds {
            if k != KIND_COMPLETE {
                return Err(Error::invalid(format!(
                    "behavior {name}: kind {}: a behavior applies to complete calls only, never to embeddings",
                    go_quote(k)
                )));
            }
        }
        Ok(())
    }

    /// Props returns the properties of the LlmBehavior node.
    pub fn props(&self) -> Map<String, Value> {
        to_props(self)
    }

    /// Matches reports whether the behavior applies to the call (enabled aside).
    pub fn matches(&self, c: &CallInfo) -> bool {
        if c.kind != KIND_COMPLETE {
            return false;
        }
        if c.json && !self.applies_to_json {
            return false;
        }
        let within = |list: &[String], v: &str| list.is_empty() || list.iter().any(|x| x == v);
        within(&self.aliases, &c.alias)
            && within(&self.models, &format!("{}/{}", c.provider, c.model))
            && within(&self.sources, &c.source)
            && within(&self.kinds, &c.kind)
    }
}

/// BehaviorFromProps reads a behavior from the properties of its node.
pub fn behavior_from_props(props: &Map<String, Value>) -> Result<Behavior, Error> {
    let b: Behavior = from_props(props).map_err(|source| Error::Decode {
        what: "behavior",
        source,
    })?;
    b.validate()?;
    Ok(b)
}

/// TerseBehavior is the built-in default behaviour, enabled when it is seeded: the ultra-terse answer style (an administrator
/// can turn it off or retire it; it does not reach calls that require JSON until flagged appliesToJSON).
pub fn terse_behavior() -> Behavior {
    Behavior {
        name: "terse".into(),
        description: "Ultra-terse answers (\"caveman\" style): fewer output tokens, same technical content".into(),
        instruction: "Answer tersely: drop articles, filler, pleasantries and hedging; fragments are fine; keep technical terms, code, identifiers and errors exact; never shorten security warnings or irreversible-action confirmations.".into(),
        enabled: true,
        position: POSITION_APPEND.into(),
        order: 100,
        ..Default::default()
    }
}

/// CallInfo is what a behaviour is matched against: the call as the gateway sees it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct CallInfo {
    /// Alias is the name requested ("default" when none, "" for a literal provider/model).
    pub alias: String,
    /// Provider and Model are the resolved target.
    pub provider: String,
    pub model: String,
    /// Source is llm.CallMeta.Source.
    pub source: String,
    /// Kind is complete or embed.
    pub kind: String,
    /// JSON: the call requires a JSON answer.
    pub json: bool,
}

/// Applied is the result of apply.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Applied {
    /// System is the system text to send.
    pub system: String,
    /// Names are the behaviours added, in the order they were applied (prepended ones first as they appear in the text).
    pub names: Vec<String>,
    /// Skipped are the matching behaviours dropped because the text added would pass MAX_BEHAVIOR_BYTES.
    pub skipped: Vec<String>,
    /// Bytes is the length of the text added (separators included).
    pub bytes: usize,
    /// Uses details Names: the instruction each behaviour added, to price it (ADR 0093, "Measured cost").
    pub uses: Vec<Use>,
}

/// Use is one behaviour applied to a call.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Use {
    pub name: String,
    /// Instruction is the trimmed instruction (without the JSON notice), which instruction_hash fingerprints.
    pub instruction: String,
    /// Bytes is the text it added (notice and separator included).
    pub bytes: usize,
}

/// EstimateTokens estimates the tokens of n bytes of text (four bytes a token, rounded up): the fallback when no measure
/// exists.
pub fn estimate_tokens(n: usize) -> usize {
    n.div_ceil(4)
}

/// InstructionHash fingerprints an instruction (sha256, hex): a measured cost is valid for one text only.
pub fn instruction_hash(instruction: &str) -> String {
    let h = Sha256::digest(instruction.trim().as_bytes());
    h.iter().map(|b| format!("{b:02x}")).collect()
}

impl Applied {
    /// Tokens estimates the tokens the behaviours added (four bytes a token): an estimate for the ledger, not a measure.
    pub fn tokens(&self) -> usize {
        estimate_tokens(self.bytes)
    }
}

impl Snapshot {
    /// Apply returns the system text of a call with the enabled behaviours that match it (see the free function
    /// [`apply`]).
    pub fn apply(&self, system: &str, c: &CallInfo) -> Applied {
        apply(Some(self), system, c)
    }
}

/// Apply returns the system text of a call with the enabled behaviours of a snapshot (None: none) that match it: sorted
/// by order then name, prepended ones before the system text, appended ones after, separated by blank lines. A call that
/// requires JSON gets only the behaviours flagged applies_to_json, each wrapped with JSON_NOTICE. Nothing is added to an
/// embedding.
pub fn apply(s: Option<&Snapshot>, system: &str, c: &CallInfo) -> Applied {
    let mut out = Applied {
        system: system.to_string(),
        ..Default::default()
    };
    let Some(s) = s else { return out };
    if c.kind != KIND_COMPLETE {
        return out;
    }
    let mut matching: Vec<&Behavior> = s
        .behaviors
        .iter()
        .filter(|b| b.enabled && b.matches(c))
        .collect();
    matching.sort_by(|a, b| a.order.cmp(&b.order).then_with(|| a.name.cmp(&b.name)));
    let mut pre: Vec<String> = Vec::new();
    let mut post: Vec<String> = Vec::new();
    for b in matching {
        let mut text = b.instruction.trim().to_string();
        if c.json {
            text = format!("{JSON_NOTICE}{text}");
        }
        if out.bytes + text.len() + 2 > MAX_BEHAVIOR_BYTES {
            out.skipped.push(b.name.clone());
            continue;
        }
        out.bytes += text.len() + 2;
        out.names.push(b.name.clone());
        out.uses.push(Use {
            name: b.name.clone(),
            instruction: b.instruction.trim().to_string(),
            bytes: text.len() + 2,
        });
        if b.position == POSITION_PREPEND {
            pre.push(text);
        } else {
            post.push(text);
        }
    }
    if out.names.is_empty() {
        return out;
    }
    let parts: Vec<&str> = pre
        .iter()
        .map(String::as_str)
        .chain(std::iter::once(system))
        .chain(post.iter().map(String::as_str))
        .filter(|p| !p.trim().is_empty())
        .collect();
    out.system = parts.join("\n\n");
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{STATE_RETIRED, build_snapshot, llmcfg::tests::node};

    #[test]
    fn test_behavior_round_trip_and_validation() {
        let b = Behavior {
            name: "terse".into(),
            instruction: "Be brief.".into(),
            enabled: true,
            position: POSITION_PREPEND.into(),
            order: 3,
            aliases: vec!["fast".into()],
            models: vec!["a/b".into()],
            sources: vec!["engine".into()],
            kinds: vec![KIND_COMPLETE.into()],
            applies_to_json: true,
            ..Default::default()
        };
        let got = behavior_from_props(&b.props()).unwrap();
        assert!(
            got.name == b.name
                && got.order == 3
                && got.position == POSITION_PREPEND
                && got.applies_to_json
                && got.aliases.len() == 1
                && got.models[0] == "a/b",
            "{got:?}"
        );
        let x = |f: fn(&mut Behavior)| {
            let mut b = Behavior {
                name: "x".into(),
                instruction: "x".into(),
                ..Default::default()
            };
            f(&mut b);
            b
        };
        let bad: Vec<(&str, Behavior)> = vec![
            ("name", x(|b| b.name = "Bad Name".into())),
            ("empty", x(|b| b.instruction = "  ".into())),
            (
                "too long",
                x(|b| b.instruction = "a".repeat(MAX_INSTRUCTION + 1)),
            ),
            ("position", x(|b| b.position = "middle".into())),
            ("alias", x(|b| b.aliases = vec!["A B".into()])),
            ("model", x(|b| b.models = vec!["nomodel".into()])),
            ("source", x(|b| b.sources = vec!["nobody".into()])),
            ("embed kinds", x(|b| b.kinds = vec!["embed".into()])),
        ];
        for (what, v) in bad {
            assert!(v.validate().is_err(), "{what}: accepted");
        }
        assert_eq!(
            x(|b| b.aliases = vec!["A B".into()])
                .validate()
                .unwrap_err()
                .to_string(),
            "behavior x: alias \"A B\" is not a valid alias name"
        );
    }

    #[test]
    fn test_snapshot_splits_behaviors() {
        let on = Behavior {
            name: "on".into(),
            instruction: "ON".into(),
            enabled: true,
            order: 2,
            ..Default::default()
        };
        let off = Behavior {
            name: "off".into(),
            instruction: "OFF".into(),
            ..Default::default()
        };
        let retired = Behavior {
            name: "gone".into(),
            instruction: "GONE".into(),
            enabled: true,
            ..Default::default()
        };
        let broken = Behavior {
            name: "broken".into(),
            enabled: true,
            ..Default::default()
        };
        let mut gone = node(&behavior_key("gone"), NODE_TYPE_BEHAVIOR, retired.props());
        gone.state = STATE_RETIRED.into();
        let s = build_snapshot(
            &"b".into(),
            &[
                node(&behavior_key("on"), NODE_TYPE_BEHAVIOR, on.props()),
                node(&behavior_key("off"), NODE_TYPE_BEHAVIOR, off.props()),
                gone,
                node(&behavior_key("broken"), NODE_TYPE_BEHAVIOR, broken.props()),
            ],
            &[],
        );
        assert!(
            s.behaviors.len() == 1
                && s.behaviors[0].name == "on"
                && s.off.len() == 1
                && s.off[0].name == "off",
            "{:?} {:?}",
            s.behaviors,
            s.off
        );
        assert!(
            s.problems.len() == 1 && s.problems[0].contains("LLB:broken"),
            "problems: {:?}",
            s.problems
        );
    }

    #[test]
    fn test_apply() {
        let beh =
            |name: &str, instr: &str, order: i64, position: &str, sources: &[&str]| Behavior {
                name: name.into(),
                instruction: instr.into(),
                enabled: true,
                order,
                position: position.into(),
                sources: sources.iter().map(|s| s.to_string()).collect(),
                ..Default::default()
            };
        let s = Snapshot {
            behaviors: vec![
                beh("b", "B", 1, "", &[]),
                beh("a", "A", 1, POSITION_PREPEND, &[]),
                beh("z", "Z", 0, "", &["helper"]),
            ],
            ..Default::default()
        };
        let mut call = CallInfo {
            alias: "default".into(),
            provider: "p".into(),
            model: "m".into(),
            source: "engine".into(),
            kind: KIND_COMPLETE.into(),
            json: false,
        };
        let got = s.apply("S", &call);
        assert!(
            got.system == "A\n\nS\n\nB" && got.names.join(",") == "a,b",
            "{got:?}"
        );
        let got = s.apply("", &call);
        assert_eq!(got.system, "A\n\nB", "no system text");
        call.kind = "embed".into();
        let got = s.apply("S", &call);
        assert!(got.system == "S" && got.names.is_empty(), "embed: {got:?}");
        let got = apply(
            None,
            "S",
            &CallInfo {
                kind: KIND_COMPLETE.into(),
                ..Default::default()
            },
        );
        assert_eq!(got.system, "S");
    }

    #[test]
    fn test_terse_behavior_is_valid_and_on() {
        let b = terse_behavior();
        b.validate().unwrap();
        assert!(b.enabled);
    }

    #[test]
    fn test_instruction_hash() {
        assert_eq!(
            instruction_hash("  abc \n"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        assert_eq!(estimate_tokens(5), 2);
    }
}
