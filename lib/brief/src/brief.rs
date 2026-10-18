//! The brief of a change and the trace of an information through it.

use std::cmp::Ordering;
use std::collections::HashMap;
use std::fmt::Write as _;

use goap_domain::{
    Blackboard, Change, ChangeItem, ITEM_REJECTED, ItemID, QUESTION_OPEN, TYPE_ARTIFACT, TYPE_SEP,
    active_option_of, decision_points_of,
};
use serde_json::{Map, Value};

/// The errors of the package.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// Trace found nothing of the reference on the change.
    #[error(
        "nothing named {} on the change (an item id, a risk or action key, or a node key)",
        go_quote(.0)
    )]
    NothingNamed(String),
}

/// Step is the step of a process the reader carries out.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Step {
    pub process: String,
    pub path: String,
    pub method: String,
    pub responsible: String,
    pub accountable: String,
}

// Limits bound the brief.
const MAX_IMPACTS: usize = 40;
const MAX_ARTIFACTS: usize = 8;
const MAX_RISKS: usize = 15;
const MAX_ACTIONS: usize = 15;
const MAX_TEXT: usize = 90;
/// long enough for a description being rated (ADR 0110)
const MAX_INTENT: usize = 1500;
const MAX_THREAD: usize = 12;

/// ArtifactTypesSkipped are the bookkeeping artifacts the brief leaves out.
pub const ARTIFACT_TYPES_SKIPPED: &[&str] = &["guidance"];

fn short(s: &str, n: usize) -> String {
    let s = s.split_whitespace().collect::<Vec<_>>().join(" ");
    let r: Vec<char> = s.chars().collect();
    if r.len() > n {
        let mut out: String = r[..n - 1].iter().collect();
        out.push('…');
        return out;
    }
    s
}

/// typeName drops the namespace of a qualified type when it is the change's.
fn type_name<'a>(t: &'a str, ns: &str) -> &'a str {
    match t.split_once(TYPE_SEP) {
        Some((n, rest)) if n == ns => rest,
        _ => t,
    }
}

fn or<'a>(s: &'a str, dflt: &'a str) -> &'a str {
    if s.is_empty() { dflt } else { s }
}

/// Of renders the brief of a change as seen on a blackboard, for a reader carrying out step (None: none).
pub fn of(bb: &Blackboard, step: Option<&Step>) -> String {
    let c = &bb.change;
    let mut b = String::new();
    macro_rules! line {
        ($($arg:tt)*) => {{
            let _ = write!(b, $($arg)*);
            b.push('\n');
        }};
    }
    line!(
        "CHANGE {} {} status={} ns={} goal={}",
        short_id(&c.id),
        go_quote(&short(&c.title, MAX_TEXT)),
        c.status,
        c.namespace,
        c.goal
    );
    if !c.intent.is_empty() {
        line!("INTENT {}", short(&c.intent, MAX_INTENT));
    }
    // the fingerprint of the description, what a rater copies in rated (ADR 0110)
    line!(
        "SUBJECT {} fp={}",
        goap_assess::SUBJECT_DESCRIPTION,
        goap_assess::fingerprint_of(bb, goap_assess::SUBJECT_DESCRIPTION)
    );
    if let Some(step) = step {
        let mut s = format!("STEP {}", step.path);
        if !step.method.is_empty() {
            s += &format!(" method={}", step.method);
        }
        if !step.responsible.is_empty() {
            s += &format!(" R={}", step.responsible);
        }
        if !step.accountable.is_empty() {
            s += &format!(" A={}", step.accountable);
        }
        line!("{s}");
    }
    let option = active_option_of(bb);
    if !option.is_empty() {
        line!("OPTION {option}");
    }

    // change impacts: the node, its type, what the change does, how far it went, the review
    let impacts: Vec<_> = c.nodes.iter().filter(|n| !n.superseded).collect();
    if !impacts.is_empty() {
        line!(
            "IMPACTS {} (key type intent state review fp)",
            impacts.len()
        );
        for (i, n) in impacts.iter().enumerate() {
            if i == MAX_IMPACTS {
                line!("- … {} more", impacts.len() - MAX_IMPACTS);
                break;
            }
            let state = if n.landed.is_some() {
                "landed"
            } else if n.drafted() {
                "drafted"
            } else if n.post.is_some() {
                "written"
            } else {
                "planned"
            };
            let mut s = format!(
                "- {} {} {} {} {} fp={}",
                n.key,
                type_name(&n.r#type, &c.namespace),
                n.intent,
                state,
                n.review,
                goap_assess::fingerprint_of(
                    bb,
                    &format!("{}{}", goap_assess::IMPACT_PREFIX, n.key)
                )
            );
            if !n.rationale.is_empty() {
                s += &format!(" — {}", short(&n.rationale, 60));
            }
            line!("{s}");
        }
    }

    // decision points and their open questions
    for d in decision_points_of(bb) {
        if d.status == "decided" {
            line!(
                "DECIDED {} {} → {}",
                d.id,
                go_quote(&short(&d.question, 70)),
                d.option
            );
            continue;
        }
        let mut s = format!(
            "DECISION {} {} {}",
            d.id,
            d.status,
            go_quote(&short(&d.question, 70))
        );
        if !d.options.is_empty() {
            s += &format!(" options={}", d.options.join("|"));
        }
        line!("{s}");
        for q in &d.questions {
            if q.status == QUESTION_OPEN {
                line!("- question {} {}", q.id, go_quote(&short(&q.text, 70)));
            }
        }
    }

    // the risk register, the live and the highest first
    let mut risks = goap_risk::risks(c);
    if !risks.is_empty() {
        risks.sort_by(|a, b| {
            if a.live() != b.live() {
                return if a.live() {
                    Ordering::Less
                } else {
                    Ordering::Greater
                };
            }
            b.score().cmp(&a.score())
        });
        line!("RISKS {} (key score=PxI status owner title)", risks.len());
        for (i, r) in risks.iter().enumerate() {
            if i == MAX_RISKS {
                line!("- … {} more", risks.len() - MAX_RISKS);
                break;
            }
            let mut s = format!(
                "- {} {}={}x{} {} {} {}",
                r.key,
                r.score(),
                r.probability,
                r.impact,
                r.status,
                or(&r.owner, "-"),
                go_quote(&short(&r.title, 70))
            );
            if !r.actions.is_empty() {
                s += &format!(" actions={}", r.actions.join(","));
            }
            line!("{s}");
        }
    }
    let mut open = Vec::new();
    let mut done = 0;
    for a in goap_risk::actions(c) {
        if a.status == goap_risk::ACTION_OPEN {
            open.push(a);
        } else {
            done += 1;
        }
    }
    if !open.is_empty() || done > 0 {
        line!(
            "ACTIONS {} open, {} closed (key owner due for title)",
            open.len(),
            done
        );
        for (i, a) in open.iter().enumerate() {
            if i == MAX_ACTIONS {
                line!("- … {} more", open.len() - MAX_ACTIONS);
                break;
            }
            line!(
                "- {} {} {} {} {}",
                a.key,
                or(&a.owner, "-"),
                or(&a.due, "-"),
                or(&a.r#for, "-"),
                go_quote(&short(&a.title, 70))
            );
        }
    }

    // the ratings and the discussions of their topics (ADR 0110, 0112 §4): the rater reads what was said before it
    for a in goap_assess::assessments(bb) {
        let state = if !a.current {
            "stale: the subject changed since"
        } else if !a.answered {
            "current, a comment awaits an answer"
        } else {
            "current"
        };
        line!(
            "ASSESSMENT {} score={}/{} {} ({}) fp={}",
            a.key,
            a.score,
            goap_assess::MAX_SCORE,
            or(&a.verdict, "-"),
            state,
            or(&goap_assess::fingerprint_of(bb, &a.key), "-")
        );
        for f in &a.findings {
            line!(
                "- finding {} {}",
                or(&f.severity, "-"),
                go_quote(&short(&f.text, 160))
            );
        }
        if !a.suggestion.is_empty() {
            line!("- suggested {}", go_quote(&short(&a.suggestion, 400)));
        }
    }
    for key in thread_keys(c) {
        let msgs = goap_assess::thread(c, &key);
        line!("THREAD {} {} messages (oldest first)", key, msgs.len());
        for m in &msgs[msgs.len().saturating_sub(MAX_THREAD)..] {
            let who = if m.r#type == goap_assess::TYPE_ASSESSMENT {
                format!("rater score={}", m.score)
            } else {
                "person".to_string()
            };
            line!("- {}: {}", who, go_quote(&short(&m.text, 400)));
        }
    }

    // the latest artifacts
    let arts: Vec<&ChangeItem> = c
        .items
        .iter()
        .filter(|it| {
            it.r#type == TYPE_ARTIFACT
                && c.active(&it.id)
                && !ARTIFACT_TYPES_SKIPPED.contains(&it.target.as_str())
        })
        .collect();
    if !arts.is_empty() {
        let from = arts.len().saturating_sub(MAX_ARTIFACTS);
        line!("ARTIFACTS {} (latest: id type summary fp)", arts.len());
        for it in &arts[from..] {
            line!(
                "- {} {} {} fp={}",
                short_id(&it.id),
                or(&it.target, "-"),
                short(&summary(&it.data), MAX_TEXT),
                goap_assess::fingerprint_of(bb, &format!("{}{}", goap_assess::ITEM_PREFIX, it.id))
            );
        }
    }
    b
}

/// threadKeys are the topics of a discussion (ADR 0112 §4): the subjects someone commented on or rated more than once,
/// in the order of their first message. The discussion of a topic is what joins the runs that rate it, each of them
/// independent: the next rater reads what the earlier ones said and what the persons answered.
fn thread_keys(c: &Change) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut ratings: HashMap<String, i32> = HashMap::new();
    for it in &c.items {
        if !c.active(&it.id) || it.status == ITEM_REJECTED {
            continue;
        }
        let k = if it.r#type == goap_assess::TYPE_COMMENT {
            str_of(&it.data, "about")
        } else if it.r#type == goap_assess::TYPE_ASSESSMENT {
            let k = str_of(&it.data, "key");
            let n = ratings.entry(k.to_string()).or_default();
            *n += 1;
            if *n < 2 {
                continue;
            }
            k
        } else {
            continue;
        };
        if !k.is_empty() && !out.iter().any(|o| o == k) {
            out.push(k.to_string());
        }
    }
    out
}

fn str_of<'a>(d: &'a Map<String, Value>, key: &str) -> &'a str {
    d.get(key).and_then(Value::as_str).unwrap_or("")
}

/// summary is the most telling text of an artifact's data.
fn summary(d: &Map<String, Value>) -> String {
    for k in ["summary", "text", "title", "markdown", "reason"] {
        if let Some(Value::String(s)) = d.get(k)
            && !s.trim().is_empty()
        {
            return s.clone();
        }
    }
    let mut keys: Vec<&str> = d.keys().map(String::as_str).collect();
    keys.sort_unstable();
    format!("{{{}}}", keys.join(","))
}

fn short_id(id: &str) -> &str {
    // Go slices bytes; ids are ASCII, so cut at a char boundary at most 8 bytes in
    if id.len() > 8 {
        let mut end = 8;
        while !id.is_char_boundary(end) {
            end -= 1;
        }
        return &id[..end];
    }
    id
}

/// Trace follows an information through a change: an item (by id or id prefix, or a risk / action key) or a node (by
/// key). It says what produced it, what it derives from, what derives from it, and what replaced it.
pub fn trace(c: &Change, r#ref: &str) -> Result<String, Error> {
    let mut b = String::new();
    macro_rules! line {
        ($($arg:tt)*) => {{
            let _ = write!(b, $($arg)*);
            b.push('\n');
        }};
    }
    let by_id: HashMap<&ItemID, &ChangeItem> = c.items.iter().map(|it| (&it.id, it)).collect();
    let describe = |it: &ChangeItem| -> String {
        let mut s = format!("{} {}", short_id(&it.id), it.r#type);
        if !it.target.is_empty() {
            s += &format!("/{}", it.target);
        }
        let k = str_of(&it.data, "key");
        if !k.is_empty() {
            s += &format!(" {k}");
        }
        s += &format!(" [{}] by {}", it.status, or(&it.produced_by, "?"));
        if !it.execution.is_empty() {
            s += &format!(" run {}", short_id(&it.execution));
        }
        let t = summary(&it.data);
        if !t.is_empty() {
            s += &format!(" — {}", short(&t, 70));
        }
        s
    };
    // a node of the change
    if let Some(n) = c.nodes.iter().find(|n| n.key == r#ref) {
        line!(
            "NODE {} {} {} review={}",
            n.key,
            n.r#type,
            n.intent,
            n.review
        );
        line!(
            "declared by {}{} — {}",
            or(&n.produced_by, "?"),
            run_of(&n.execution),
            short(&n.rationale, 120)
        );
        for id in n.derived_from.iter().chain(&n.items) {
            if let Some(it) = by_id.get(id) {
                line!("from {}", describe(it));
            }
        }
        if n.drafted() {
            line!("drafted (no version until the change lands)");
        } else if let Some(post) = &n.post {
            line!("written {post}");
        }
        for r in &n.reviews {
            line!(
                "reviewed {} by {}: {}",
                r.status,
                r.by,
                short(&r.comment, 80)
            );
        }
        if let Some(landed) = &n.landed {
            line!("landed {landed}");
        }
        return Ok(b);
    }
    // an item: by id, id prefix, or record key (every version)
    let hits: Vec<&ChangeItem> = c
        .items
        .iter()
        .filter(|it| {
            let k = str_of(&it.data, "key");
            it.id.as_str() == r#ref
                || (r#ref.len() >= 6 && it.id.starts_with(r#ref))
                || (!k.is_empty() && k == r#ref)
        })
        .collect();
    if hits.is_empty() {
        return Err(Error::NothingNamed(r#ref.to_string()));
    }
    for it in hits {
        line!("ITEM {}", describe(it));
        for id in &it.derived_from {
            if let Some(src) = by_id.get(id) {
                line!("from {}", describe(src));
            }
        }
        for other in &c.items {
            if other.derived_from.contains(&it.id) {
                line!("led to {}", describe(other));
            }
            if other.supersedes.contains(&it.id) {
                line!("replaced by {}", describe(other));
            }
        }
        for n in &c.nodes {
            if n.items.contains(&it.id) || n.derived_from.contains(&it.id) {
                line!("led to node {} ({} {})", n.key, n.intent, n.review);
            }
        }
    }
    Ok(b)
}

fn run_of(execution: &str) -> String {
    if execution.is_empty() {
        return String::new();
    }
    format!(" run {}", short_id(execution))
}

/// Go's `strconv.Quote` (`%q`): the printable runes as they are, the others escaped.
pub(crate) fn go_quote(s: &str) -> String {
    let mut b = String::with_capacity(s.len() + 2);
    b.push('"');
    for c in s.chars() {
        match c {
            '"' => b.push_str("\\\""),
            '\\' => b.push_str("\\\\"),
            '\u{7}' => b.push_str("\\a"),
            '\u{8}' => b.push_str("\\b"),
            '\u{c}' => b.push_str("\\f"),
            '\n' => b.push_str("\\n"),
            '\r' => b.push_str("\\r"),
            '\t' => b.push_str("\\t"),
            '\u{b}' => b.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => {
                let _ = write!(b, "\\x{:02x}", c as u32);
            }
            c if !is_print(c) => {
                if (c as u32) < 0x10000 {
                    let _ = write!(b, "\\u{:04x}", c as u32);
                } else {
                    let _ = write!(b, "\\U{:08x}", c as u32);
                }
            }
            c => b.push(c),
        }
    }
    b.push('"');
    b
}

/// Go's `strconv.IsPrint` for the runes a text commonly holds: controls, the spaces other than U+0020, the format
/// characters, the private use areas and the noncharacters are not printable (unassigned code points are not told).
fn is_print(c: char) -> bool {
    let u = c as u32;
    if c.is_control() {
        return false;
    }
    if u != 0x20 && c.is_whitespace() {
        return false;
    }
    !matches!(u,
        0xAD | 0x600..=0x605 | 0x61C | 0x6DD | 0x70F | 0x890..=0x891 | 0x8E2 | 0x180E
        | 0x200B..=0x200F | 0x202A..=0x202E | 0x2060..=0x2064 | 0x2066..=0x206F
        | 0xD800..=0xDFFF | 0xE000..=0xF8FF | 0xFDD0..=0xFDEF | 0xFEFF | 0xFFF9..=0xFFFB
        | 0x110BD | 0x110CD | 0x13430..=0x1343F | 0x1BCA0..=0x1BCA3 | 0x1D173..=0x1D17A
        | 0xE0001 | 0xE0020..=0xE007F | 0xF0000..=0x10FFFF)
        && (u & 0xFFFE) != 0xFFFE
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_domain::ITEM_ACCEPTED;
    use serde_json::json;

    // The discussion of a topic joins the independent runs that rate it (ADR 0112 §4): two ratings and no comment are
    // already a thread the next rater reads.
    #[test]
    fn test_brief_gives_the_discussion_of_a_rated_topic() {
        let rating = |id: &str, score: i64, reply: &str| ChangeItem {
            id: id.into(),
            r#type: goap_assess::TYPE_ASSESSMENT,
            status: ITEM_ACCEPTED,
            data: json!({"key": "description", "score": score, "reply": reply})
                .as_object()
                .cloned()
                .unwrap(),
            ..Default::default()
        };
        let mut c = Change {
            id: "C1".into(),
            title: "Note".into(),
            intent: "write a note".into(),
            items: vec![
                rating("a", 2, "say which release"),
                rating("b", 4, "clear now"),
            ],
            ..Default::default()
        };
        let bb = |c: &Change| Blackboard {
            change: c.clone(),
            ..Default::default()
        };
        let got = of(&bb(&c), None);
        assert!(
            got.contains("THREAD description 2 messages") && got.contains("say which release"),
            "brief:\n{got}"
        );
        c.items.truncate(1);
        let got = of(&bb(&c), None);
        assert!(
            !got.contains("THREAD"),
            "one rating is no discussion yet:\n{got}"
        );
    }

    /// The brief and the traces of a rich change, byte for byte what the Go wrote (testdata/, from the Go package).
    #[test]
    fn test_golden() {
        goap_risk::register();
        goap_assess::register();
        let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/testdata/");
        let read = |f: &str| std::fs::read_to_string(format!("{dir}{f}")).unwrap();
        let c: Change = serde_json::from_str(&read("change.json")).unwrap();
        let bb = Blackboard {
            change: c.clone(),
            ..Default::default()
        };
        let step = Step {
            path: "p/s".into(),
            method: "m".into(),
            responsible: "w".into(),
            ..Default::default()
        };
        assert_eq!(of(&bb, Some(&step)), read("brief.txt"));
        assert_eq!(trace(&c, "it-art-0001").unwrap(), read("trace.txt"));
        assert_eq!(trace(&c, "REQ-2").unwrap(), read("trace2.txt"));
    }

    #[test]
    fn test_go_quote() {
        assert_eq!(
            go_quote("a\"b\\\n\u{1}é\u{a0}\u{200b}"),
            "\"a\\\"b\\\\\\n\\x01é\\u00a0\\u200b\""
        );
        assert_eq!(short("  a  b\tc ", 90), "a b c");
        assert_eq!(short("abcdef", 4), "abc…");
    }

    #[test]
    fn test_trace() {
        let c = Change {
            items: vec![ChangeItem {
                id: "item-123456".into(),
                r#type: TYPE_ARTIFACT,
                target: "note".into(),
                data: json!({"text": "hello"}).as_object().cloned().unwrap(),
                ..Default::default()
            }],
            ..Default::default()
        };
        assert_eq!(
            trace(&c, "item-1").unwrap(),
            "ITEM item-123 artifact/note [] by ? — hello\n"
        );
        assert_eq!(
            trace(&c, "nope").unwrap_err().to_string(),
            "nothing named \"nope\" on the change (an item id, a risk or action key, or a node key)"
        );
    }
}
