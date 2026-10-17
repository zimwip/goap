//! Condensing the conversation of an action without a model: what a tool returns is tidied before the model sees it
//! (tidy), and a tool result the conversation holds again later is replaced by a short note (dedup_results). Nothing
//! is rewritten: what stays is exact, what goes can be asked for again.

use std::collections::{HashMap, HashSet};
use std::sync::LazyLock;

use goap_llm::Message;
use regex::Regex;
use serde_json::Value;
use sha2::{Digest, Sha256};

/// ansi_escape matches the terminal escapes of a command's output (colours, cursor moves, titles).
static ANSI_ESCAPE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])").unwrap()
});

/// repeats_kept is how many identical lines in a row tidy_text keeps before it counts the others.
const REPEATS_KEPT: usize = 2;

/// tidy returns v with its texts tidied (tidy_text), maps and lists walked; v is not changed.
pub(crate) fn tidy(v: &Value) -> Value {
    match v {
        Value::String(s) => Value::String(tidy_text(s)),
        Value::Object(m) => Value::Object(m.iter().map(|(k, x)| (k.clone(), tidy(x))).collect()),
        Value::Array(l) => Value::Array(l.iter().map(tidy).collect()),
        v => v.clone(),
    }
}

/// tidy_text removes what a terminal output carries for the eye only: escapes, carriage-return redraws (a progress
/// bar keeps its last state), trailing blanks; and counts the identical lines in a row past REPEATS_KEPT.
pub(crate) fn tidy_text(s: &str) -> String {
    if !s.contains(['\x1b', '\r', '\n']) {
        return s.to_string();
    }
    let s = ANSI_ESCAPE.replace_all(s, "");
    let s = s.replace("\r\n", "\n");
    let mut out: Vec<String> = Vec::new();
    let mut same = 0usize;
    fn flush(out: &mut Vec<String>, same: &mut usize) {
        if *same > 0 {
            out.push(format!("(previous line repeated {} more times)", *same));
            *same = 0;
        }
    }
    for l in s.split('\n') {
        let l = match l.rfind('\r') {
            Some(i) => &l[i + 1..],
            None => l,
        };
        let l = l.trim_end_matches([' ', '\t']);
        let n = out.len();
        if (!l.is_empty() && n >= REPEATS_KEPT && same == 0 && out[n - 1] == l && out[n - 2] == l)
            || (same > 0 && out[n - 1] == l)
        {
            same += 1;
            continue;
        }
        flush(&mut out, &mut same);
        out.push(l.to_string());
    }
    flush(&mut out, &mut same);
    out.join("\n")
}

/// min_dedup is the size under which a tool result is never replaced: a note would not be much shorter.
const MIN_DEDUP: usize = 512;

/// superseded replaces the result of a call made again later with the same arguments (a file read again).
pub(crate) const SUPERSEDED: &str =
    "(superseded: the same call was made again later, its latest result is below)";
/// repeated replaces a result identical to a later one of another call.
pub(crate) const REPEATED: &str = "(identical to a later tool result, see below)";

/// dedup_results replaces a tool result the conversation holds again later: the result of a call made again with
/// the same tool and arguments (the latest is what holds: a file read again after an edit), else a result identical
/// to a later one. Errors and short results stay. The latest occurrence always stays whole, so nothing is lost; msgs
/// is not changed. Only the native tool results are looked at.
pub(crate) fn dedup_results(msgs: &[Message]) -> Vec<Message> {
    let mut calls: HashMap<&str, String> = HashMap::new(); // call id -> tool and arguments
    for m in msgs {
        for c in &m.tool_calls {
            let raw = serde_json::to_string(&c.arguments).unwrap_or_default();
            calls.insert(&c.id, format!("{}\x00{}", c.name, raw));
        }
    }
    let mut sigs: HashSet<String> = HashSet::new();
    let mut sums: HashSet<[u8; 32]> = HashSet::new();
    let mut out = msgs.to_vec();
    for i in (1..msgs.len()).rev() {
        for j in (0..msgs[i].tool_results.len()).rev() {
            let r = &msgs[i].tool_results[j];
            if r.is_error || r.content.len() < MIN_DEDUP {
                continue;
            }
            let sig = calls.get(r.call_id.as_str()).cloned().unwrap_or_default();
            let sum: [u8; 32] = Sha256::digest(r.content.as_bytes()).into();
            let note = if !sig.is_empty() && sigs.contains(&sig) {
                SUPERSEDED
            } else if sums.contains(&sum) {
                REPEATED
            } else {
                ""
            };
            if !sig.is_empty() {
                sigs.insert(sig);
            }
            sums.insert(sum);
            if note.is_empty() {
                continue;
            }
            out[i].tool_results[j].content = note.to_string();
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_llm::{ToolCall, ToolResult};
    use serde_json::json;

    #[test]
    fn test_tidy_text() {
        let input = format!(
            "\x1b[32mok\x1b[0m   \r\nprogress 10%\rprogress 100%\n{}end",
            "same\n".repeat(6)
        );
        let want = "ok\nprogress 100%\nsame\nsame\n(previous line repeated 4 more times)\nend";
        assert_eq!(tidy_text(&input), want);
        assert_eq!(tidy_text("plain text"), "plain text");
        // blank lines are never counted, two repeats stay
        assert_eq!(tidy_text("a\na\n\n\n\nb"), "a\na\n\n\n\nb");
        let v = json!({"out": "x\x1b[1my", "list": ["a\r\n", 3]});
        let got = tidy(&v);
        assert_eq!(got, json!({"out": "xy", "list": ["a\n", 3]}));
        assert_eq!(v["out"], "x\x1b[1my");
    }

    #[test]
    fn test_dedup_results() {
        let (file1, file2, log) = ("v1 ".repeat(300), "v2 ".repeat(300), "log ".repeat(300));
        let call = |id: &str, name: &str, path: &str| Message {
            role: "assistant".into(),
            tool_calls: vec![ToolCall {
                id: id.into(),
                name: name.into(),
                arguments: json!({"path": path}).as_object().cloned().unwrap(),
            }],
            ..Default::default()
        };
        let result = |id: &str, content: &str| Message {
            role: "user".into(),
            tool_results: vec![ToolResult {
                call_id: id.into(),
                content: content.into(),
                ..Default::default()
            }],
            ..Default::default()
        };
        let msgs = vec![
            Message {
                role: "user".into(),
                content: "the task".into(),
                ..Default::default()
            },
            call("1", "workspace/read", "a.go"),
            result("1", &file1),
            call("2", "workspace/bash", "x"),
            result("2", &log),
            call("3", "workspace/read", "a.go"),
            result("3", &file2), // read again after an edit
            call("4", "workspace/bash", "y"),
            result("4", &log), // another call, same output
            call("5", "workspace/read", "b.go"),
            result("5", "short"),
            call("6", "workspace/read", "b.go"),
            result("6", "short"),
        ];
        let got = dedup_results(&msgs);
        assert_eq!(got[2].tool_results[0].content, SUPERSEDED);
        assert_eq!(got[4].tool_results[0].content, REPEATED);
        assert!(
            got[6].tool_results[0].content == file2
                && got[8].tool_results[0].content == log
                && got[10].tool_results[0].content == "short",
            "a latest or a short result was replaced"
        );
        assert!(
            msgs[2].tool_results[0].content == file1 && msgs[4].tool_results[0].content == log,
            "dedup_results changed its input"
        );
        let again = dedup_results(&got);
        assert!(
            again[2].tool_results[0].content == SUPERSEDED
                && again[6].tool_results[0].content == file2,
            "not stable"
        );
        // nothing to replace: the same messages
        assert_eq!(dedup_results(&msgs[..3]), msgs[..3].to_vec());
    }
}
