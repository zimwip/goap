//! The comparison of the Go and the Rust graph services, call by call.
//!
//! The Go service answers the client; the same call, its ids written as the Rust ones, goes to the Rust service,
//! and the two answers are compared once the Rust one is read back in Go ids. The calls go one at a time: the two
//! services see the same sequence, and an id is paired before a later call names it.
use std::{
    fs::{self, File},
    io::{BufWriter, Write},
    path::PathBuf,
};

use bytes::Bytes;
use http::{HeaderMap, HeaderValue, Method};
use serde::Serialize;
use serde_json::{Map, Value};

use crate::{
    ids::{Clash, IdMap, TIME, Toward},
    rpc::{self, Codec, Procedure, RpcError},
    upstream::{Reply, Upstream},
};

/// What a comparison found of a call.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Verdict {
    /// The same answer.
    Same,
    /// The same elements of a list, in another order.
    OrderDiffers,
    /// The same error code, another message.
    MessageDiffers,
    /// Another answer.
    Differs,
    /// An error on one side only, or another code.
    ErrorDiffers,
    /// The Rust service does not serve the method yet.
    RustUnimplemented,
    /// An answer the proxy could not read.
    Undecodable,
}

impl Verdict {
    pub fn name(self) -> &'static str {
        match self {
            Verdict::Same => "same",
            Verdict::OrderDiffers => "order_differs",
            Verdict::MessageDiffers => "message_differs",
            Verdict::Differs => "differs",
            Verdict::ErrorDiffers => "error_differs",
            Verdict::RustUnimplemented => "rust_unimplemented",
            Verdict::Undecodable => "undecodable",
        }
    }

    /// Whether the verdict is a divergence (the soft ones are counted, not failed on).
    pub fn diverges(self) -> bool {
        matches!(self, Verdict::Differs | Verdict::ErrorDiffers | Verdict::Undecodable)
    }
}

/// A difference of two answers.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Diff {
    pub path: String,
    pub go: Value,
    pub rust: Value,
}

/// The record of a compared call, a line of `calls.jsonl`.
#[derive(Debug, Serialize)]
pub struct Record {
    pub seq: u64,
    pub at: String,
    pub procedure: String,
    pub verdict: Verdict,
    pub go_ms: f64,
    pub rust_ms: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub go_error: Option<RpcError>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rust_error: Option<RpcError>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub diffs: Vec<Diff>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub clashes: Vec<Clash>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub notes: Vec<String>,
    /// The request and the two answers (Rust in Go ids) of a divergent call, cut at [`MAX_SAMPLE`] bytes.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sample: Option<Value>,
}

const MAX_DIFFS: usize = 20;
const MAX_SAMPLE: usize = 16 << 10;
const MAX_VALUE: usize = 300;

/// A field left out of the comparison: `[<Method>:]<path>`, the path dotted, lists left out of it (`node.links.id`
/// is the `id` of every link); a path matches the end of a path (`createdAt` is every `createdAt`).
#[derive(Debug, Clone)]
pub struct Ignore {
    method: Option<String>,
    path: String,
}

impl Ignore {
    pub fn parse(s: &str) -> Ignore {
        match s.split_once(':') {
            Some((m, p)) => Ignore {
                method: Some(m.to_string()),
                path: p.to_string(),
            },
            None => Ignore {
                method: None,
                path: s.to_string(),
            },
        }
    }

    fn matches(&self, method: &str, path: &str) -> bool {
        if self.method.as_deref().is_some_and(|m| m != method) {
            return false;
        }
        path == self.path || path.ends_with(&format!(".{}", self.path))
    }
}

/// The value as compared: times masked (each side stamps its own), ignored fields left out.
pub fn normalise(v: &Value, method: &str, ignore: &[Ignore]) -> Value {
    fn walk(v: &Value, path: &str, method: &str, ignore: &[Ignore]) -> Value {
        match v {
            Value::String(s) if TIME.is_match(s) => Value::String("<time>".into()),
            // a JSON text (the payload of a log entry) is compared as what it holds, its times masked too
            Value::String(s) if s.starts_with(['{', '[']) => match serde_json::from_str::<Value>(s) {
                Ok(x) => {
                    let mut m = Map::new();
                    m.insert("<json>".into(), walk(&x, path, method, ignore));
                    Value::Object(m)
                }
                Err(_) => v.clone(),
            },
            Value::Array(a) => Value::Array(a.iter().map(|x| walk(x, path, method, ignore)).collect()),
            Value::Object(o) => {
                let mut out = Map::new();
                for (k, x) in o {
                    let p = if path.is_empty() {
                        k.clone()
                    } else {
                        format!("{path}.{k}")
                    };
                    if ignore.iter().any(|i| i.matches(method, &p)) {
                        continue;
                    }
                    out.insert(k.clone(), walk(x, &p, method, ignore));
                }
                Value::Object(out)
            }
            other => other.clone(),
        }
    }
    walk(v, "", method, ignore)
}

fn cut(v: &Value) -> Value {
    let s = v.to_string();
    if s.len() <= MAX_VALUE {
        v.clone()
    } else {
        let mut end = MAX_VALUE;
        while !s.is_char_boundary(end) {
            end -= 1;
        }
        Value::String(format!("{}…", &s[..end]))
    }
}

/// The differences of two values, at most [`MAX_DIFFS`].
pub fn diff(go: &Value, rust: &Value) -> Vec<Diff> {
    fn walk(path: &str, g: &Value, r: &Value, out: &mut Vec<Diff>) {
        if out.len() >= MAX_DIFFS || g == r {
            return;
        }
        match (g, r) {
            (Value::Object(a), Value::Object(b)) => {
                for (k, x) in a {
                    let p = format!("{path}.{k}");
                    walk(&p, x, b.get(k).unwrap_or(&Value::Null), out);
                }
                for (k, y) in b {
                    if !a.contains_key(k) {
                        walk(&format!("{path}.{k}"), &Value::Null, y, out);
                    }
                }
            }
            (Value::Array(a), Value::Array(b)) if a.len() == b.len() => {
                for (i, (x, y)) in a.iter().zip(b).enumerate() {
                    walk(&format!("{path}[{i}]"), x, y, out);
                }
            }
            _ => out.push(Diff {
                path: if path.is_empty() { ".".into() } else { path.to_string() },
                go: cut(g),
                rust: cut(r),
            }),
        }
    }
    let mut out = Vec::new();
    walk("", go, rust, &mut out);
    out
}

/// The value with every list sorted: two values equal so hold the same elements, whatever their order.
fn unordered(v: &Value) -> Value {
    match v {
        Value::Array(a) => {
            let mut items: Vec<Value> = a.iter().map(unordered).collect();
            items.sort_by_cached_key(|x| x.to_string());
            Value::Array(items)
        }
        Value::Object(o) => Value::Object(o.iter().map(|(k, x)| (k.clone(), unordered(x))).collect()),
        other => other.clone(),
    }
}

fn unimplemented(r: &Reply) -> bool {
    rpc::error_of(r.status, &r.body).is_some_and(|e| e.code == "unimplemented")
}

/// The comparer: the pairs of ids and the report, behind the lock that sends the calls one at a time.
pub struct Comparer {
    pub ids: IdMap,
    ignore: Vec<Ignore>,
    dir: PathBuf,
    calls: Option<BufWriter<File>>,
    seq: u64,
    clashes: u64,
    /// `GOAP_PROXY_SAMPLES=all`: the request and the answers of every call in its record, not only of a divergent one.
    samples: bool,
    /// Both services on one database (mode shadow): an id is the same on both sides, nothing is paired or translated.
    shared: bool,
}

impl Comparer {
    pub fn new(dir: PathBuf, ignore: &[String], shared: bool) -> anyhow::Result<Comparer> {
        fs::create_dir_all(&dir)?;
        let calls = File::options()
            .create(true)
            .append(true)
            .open(dir.join("calls.jsonl"))?;
        Ok(Comparer {
            ids: IdMap::default(),
            ignore: ignore.iter().map(|s| Ignore::parse(s)).collect(),
            dir,
            calls: Some(BufWriter::new(calls)),
            seq: 0,
            clashes: 0,
            samples: std::env::var("GOAP_PROXY_SAMPLES").is_ok_and(|v| v == "all"),
            shared,
        })
    }

    /// A comparer writing nothing (tests).
    #[cfg(test)]
    pub fn in_memory(ignore: &[&str]) -> Comparer {
        Comparer {
            ids: IdMap::default(),
            ignore: ignore.iter().map(|s| Ignore::parse(s)).collect(),
            dir: PathBuf::new(),
            calls: None,
            seq: 0,
            clashes: 0,
            samples: false,
            shared: false,
        }
    }

    pub fn clashes(&self) -> u64 {
        self.clashes
    }

    /// Writes the pairs of ids (`ids.json`, read by `goap-proxy diff-db`).
    pub fn save_ids(&self) -> anyhow::Result<PathBuf> {
        let path = self.dir.join("ids.json");
        let tmp = self.dir.join("ids.json.tmp");
        serde_json::to_writer(BufWriter::new(File::create(&tmp)?), &self.ids.pairs())?;
        fs::rename(&tmp, &path)?;
        Ok(path)
    }

    /// The request for the Rust side: its ids written as the Rust ones.
    pub fn translate_request(
        &self,
        proc: Option<&Procedure>,
        path: &str,
        headers: &HeaderMap,
        body: &Bytes,
    ) -> (String, HeaderMap, Bytes, Vec<String>) {
        let mut notes = Vec::new();
        if self.shared {
            return (path.to_string(), headers.clone(), body.clone(), notes);
        }
        let path = self.ids.text(path, Toward::Rust).into_owned();
        let mut h = HeaderMap::with_capacity(headers.len());
        for (k, v) in headers {
            let v = match v.to_str() {
                Ok(s) => HeaderValue::from_str(&self.ids.text(s, Toward::Rust)).unwrap_or_else(|_| v.clone()),
                Err(_) => v.clone(),
            };
            h.append(k.clone(), v);
        }
        let body = match (proc, Codec::of(headers)) {
            (Some(p), Some(c)) if !body.is_empty() => match rpc::to_json(&p.input(), c, body) {
                Ok(v) => {
                    let v = self.ids.json(&v, Toward::Rust);
                    match rpc::from_json(&p.input(), c, &v) {
                        Ok(b) => b,
                        Err(e) => {
                            notes.push(format!("request not encoded again: {e:#}"));
                            body.clone()
                        }
                    }
                }
                Err(e) => {
                    notes.push(format!("request not decoded: {e:#}"));
                    body.clone()
                }
            },
            _ => body.clone(),
        };
        (path, h, body, notes)
    }

    /// Compares the two answers of a call, pairing the ids they hold, and records it.
    pub fn judge(
        &mut self,
        proc: &Procedure,
        request: &Bytes,
        req_headers: &HeaderMap,
        go: &Reply,
        rust: &Reply,
        mut notes: Vec<String>,
    ) -> Record {
        self.seq += 1;
        let mut rec = Record {
            seq: self.seq,
            at: chrono::Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
            procedure: proc.to_string(),
            verdict: Verdict::Same,
            go_ms: go.elapsed.as_secs_f64() * 1e3,
            rust_ms: rust.elapsed.as_secs_f64() * 1e3,
            go_error: rpc::error_of(go.status, &go.body),
            rust_error: rpc::error_of(rust.status, &rust.body),
            diffs: Vec::new(),
            clashes: Vec::new(),
            notes: Vec::new(),
            sample: None,
        };
        let method = proc.method.as_str();
        let mut sample: Option<(Value, Value)> = None;
        rec.verdict = match (&rec.go_error, &rec.rust_error) {
            (go_err, Some(_)) if unimplemented(rust) && !go_err.as_ref().is_some_and(|e| e.code == "unimplemented") => {
                Verdict::RustUnimplemented
            }
            (Some(g), Some(r)) if g.code == r.code => {
                if !self.shared {
                    rec.clashes.extend(self.ids.pair_text(&g.message, &r.message));
                }
                if self.ids.text(&r.message, Toward::Go) == g.message {
                    Verdict::Same
                } else {
                    Verdict::MessageDiffers
                }
            }
            (Some(_), _) | (_, Some(_)) => Verdict::ErrorDiffers,
            (None, None) => {
                let codec = Codec::of(&go.headers).or_else(|| Codec::of(req_headers));
                let rcodec = Codec::of(&rust.headers).or(codec);
                match (codec, rcodec) {
                    (Some(gc), Some(rc)) => match (
                        rpc::to_json(&proc.output(), gc, &go.body),
                        rpc::to_json(&proc.output(), rc, &rust.body),
                    ) {
                        (Ok(g), Ok(r)) => {
                            if !self.shared {
                                rec.clashes.extend(self.ids.pair_json(&g, &r));
                            }
                            // the ids left unpaired (elements telling nothing of which is which) as ids, on two databases
                            let (gm, rm) = (
                                normalise(&self.ids.masked(&g, Toward::Go), method, &self.ignore),
                                normalise(&self.ids.masked(&r, Toward::Rust), method, &self.ignore),
                            );
                            let r = self.ids.json(&r, Toward::Go);
                            let (g, r) = (normalise(&g, method, &self.ignore), normalise(&r, method, &self.ignore));
                            let masked = !self.shared;
                            let verdict = if g == r || (masked && gm == rm) {
                                Verdict::Same
                            } else if unordered(&g) == unordered(&r) || (masked && unordered(&gm) == unordered(&rm)) {
                                Verdict::OrderDiffers
                            } else {
                                rec.diffs = diff(&g, &r);
                                Verdict::Differs
                            };
                            sample = Some((g, r));
                            verdict
                        }
                        (g, r) => {
                            for e in [g.err(), r.err()].into_iter().flatten() {
                                notes.push(format!("answer not decoded: {e:#}"));
                            }
                            Verdict::Undecodable
                        }
                    },
                    _ => {
                        if go.body == rust.body {
                            Verdict::Same
                        } else {
                            Verdict::Differs
                        }
                    }
                }
            }
        };
        // the positions and other headers of the graph (`X-Goap-*`)
        for (k, gv) in &go.headers {
            if !k.as_str().starts_with("x-goap-") {
                continue;
            }
            let (Ok(g), r) = (gv.to_str(), rust.headers.get(k).and_then(|v| v.to_str().ok())) else {
                continue;
            };
            let r = r.unwrap_or("");
            // a watermark is a map: its entries in the order of their positions, as either side writes them
            let (g, r) = if k.as_str() == "x-goap-change-seq" {
                (by_position(g), by_position(r))
            } else {
                (g.to_string(), r.to_string())
            };
            let (g, r) = (g.as_str(), r.as_str());
            if !self.shared {
                rec.clashes.extend(self.ids.pair_text(g, r));
            }
            let r = self.ids.text(r, Toward::Go);
            if g != r && !self.ignore.iter().any(|i| i.matches(method, &format!("header:{k}"))) {
                rec.diffs.push(Diff {
                    path: format!("header:{k}"),
                    go: Value::String(g.into()),
                    rust: Value::String(r.into_owned()),
                });
                if rec.verdict == Verdict::Same || rec.verdict == Verdict::OrderDiffers {
                    rec.verdict = Verdict::Differs;
                }
            }
        }
        self.clashes += rec.clashes.len() as u64;
        rec.notes = notes;
        if rec.verdict.diverges() || !rec.clashes.is_empty() || self.samples {
            let request = Codec::of(req_headers)
                .and_then(|c| rpc::to_json(&proc.input(), c, request).ok())
                .unwrap_or(Value::Null);
            let (g, r) = sample.unwrap_or_else(|| {
                (
                    Value::String(String::from_utf8_lossy(&go.body).into()),
                    Value::String(String::from_utf8_lossy(&rust.body).into()),
                )
            });
            let s = serde_json::json!({"request": request, "go": g, "rust": r});
            rec.sample = Some(if s.to_string().len() > MAX_SAMPLE {
                cut_sample(&s)
            } else {
                s
            });
        }
        if let Some(w) = self.calls.as_mut() {
            let line = serde_json::to_string(&rec).unwrap_or_default();
            if let Err(e) = writeln!(w, "{line}").and_then(|_| w.flush()) {
                tracing::warn!(error = %e, "writing calls.jsonl");
            }
        }
        rec
    }
}

fn cut_sample(s: &Value) -> Value {
    let mut out = Map::new();
    if let Value::Object(o) = s {
        for (k, v) in o {
            let t = v.to_string();
            let max = MAX_SAMPLE / 3;
            out.insert(
                k.clone(),
                if t.len() <= max {
                    v.clone()
                } else {
                    let mut end = max;
                    while !t.is_char_boundary(end) {
                        end -= 1;
                    }
                    Value::String(format!("{}…", &t[..end]))
                },
            );
        }
    }
    Value::Object(out)
}

/// Sends a call to both services and compares their answers; the Go answer is the one the client gets.
#[allow(clippy::too_many_arguments)]
pub async fn call(
    comparer: &tokio::sync::Mutex<Comparer>,
    go: &Upstream,
    rust: &Upstream,
    proc: &Procedure,
    method: &Method,
    path: &str,
    headers: &HeaderMap,
    body: Bytes,
) -> (Reply, Record) {
    let mut c = comparer.lock().await;
    let go_reply = go.call(method, path, headers, body.clone()).await;
    let (rpath, rheaders, rbody, notes) = c.translate_request(Some(proc), path, headers, &body);
    let rust_reply = rust.call(method, &rpath, &rheaders, rbody).await;
    let rec = c.judge(proc, &body, headers, &go_reply, &rust_reply, notes);
    (go_reply, rec)
}

/// The entries `<change>:<seq>` of a watermark sorted by their position.
fn by_position(v: &str) -> String {
    let mut e: Vec<&str> = v.split(',').filter(|e| !e.is_empty()).collect();
    e.sort_by_key(|e| e.rsplit_once(':').and_then(|(_, n)| n.parse::<i64>().ok()).unwrap_or(0));
    e.join(",")
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::{StatusCode, header::CONTENT_TYPE};
    use serde_json::json;
    use std::time::Duration;

    fn reply(status: u16, body: Value) -> Reply {
        let mut headers = HeaderMap::new();
        headers.insert(CONTENT_TYPE, "application/json".parse().unwrap());
        Reply {
            status: StatusCode::from_u16(status).unwrap(),
            headers,
            body: Bytes::from(body.to_string()),
            elapsed: Duration::from_millis(1),
        }
    }

    fn proc() -> Procedure {
        Procedure::of("/goap.graph.v1.GraphService/GetNode").unwrap()
    }

    fn json_headers() -> HeaderMap {
        let mut h = HeaderMap::new();
        h.insert(CONTENT_TYPE, "application/json".parse().unwrap());
        h
    }

    const G: &str = "11111111-1111-4111-8111-111111111111";
    const R: &str = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

    #[test]
    fn the_same_answer_in_other_ids_and_times_is_the_same() {
        let mut c = Comparer::in_memory(&[]);
        let go = reply(
            200,
            json!({"view": {"node": {"id": G, "key": "R-1", "createdAt": "2026-10-23T10:00:00.123Z"}}}),
        );
        let rust = reply(
            200,
            json!({"view": {"node": {"id": R, "key": "R-1", "createdAt": "2026-10-23T10:00:01Z"}}}),
        );
        let rec = c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &rust, vec![]);
        assert_eq!(rec.verdict, Verdict::Same, "{rec:?}");
        // the next request names the Go id: the Rust side gets its own
        let (_, _, body, _) = c.translate_request(
            Some(&proc()),
            "/x",
            &json_headers(),
            &Bytes::from(json!({"ref": {"id": G}}).to_string()),
        );
        assert_eq!(serde_json::from_slice::<Value>(&body).unwrap()["ref"]["id"], R);
    }

    #[test]
    fn a_json_text_is_compared_as_what_it_holds() {
        let mut c = Comparer::in_memory(&[]);
        let log = Procedure::of("/goap.graph.v1.GraphService/ListChangeLog").unwrap();
        let go = reply(
            200,
            json!({"entries": [{"payload": format!(r#"{{"op": "x", "at": "2026-10-23T10:00:00Z", "id": "{G}"}}"#)}]}),
        );
        let rust = reply(
            200,
            json!({"entries": [{"payload": format!(r#"{{"op": "x", "at": "2026-10-23T10:00:02Z", "id": "{R}"}}"#)}]}),
        );
        let rec = c.judge(&log, &Bytes::new(), &json_headers(), &go, &rust, vec![]);
        assert_eq!(rec.verdict, Verdict::Same, "{rec:?}");
        let other = reply(200, json!({"entries": [{"payload": r#"{"op": "y"}"#}]}));
        let rec = c.judge(&log, &Bytes::new(), &json_headers(), &go, &other, vec![]);
        assert_ne!(rec.verdict, Verdict::Same, "{rec:?}");
    }

    #[test]
    fn on_one_database_an_id_is_never_paired() {
        let mut c = Comparer::in_memory(&[]);
        c.shared = true;
        let go = reply(200, json!({"view": {"node": {"id": G}}}));
        let rust = reply(200, json!({"view": {"node": {"id": R}}}));
        let rec = c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &rust, vec![]);
        assert_eq!(rec.verdict, Verdict::Differs, "{rec:?}");
        assert_eq!(c.ids.len(), 0);
        let same = reply(200, json!({"view": {"node": {"id": G}}}));
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &same, vec![])
                .verdict,
            Verdict::Same
        );
    }

    #[test]
    fn another_value_differs_and_says_where() {
        let mut c = Comparer::in_memory(&[]);
        let go = reply(
            200,
            json!({"view": {"node": {"key": "R-1", "type": "alm@Requirement"}}}),
        );
        let rust = reply(
            200,
            json!({"view": {"node": {"key": "R-2", "type": "alm@Requirement"}}}),
        );
        let rec = c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &rust, vec![]);
        assert_eq!(rec.verdict, Verdict::Differs);
        assert_eq!(rec.diffs[0].path, ".view.node.key");
        assert!(rec.sample.is_some());
    }

    #[test]
    fn an_ignored_field_and_another_order() {
        let mut c = Comparer::in_memory(&["GetNode:view.node.key"]);
        let go = reply(200, json!({"view": {"node": {"key": "R-1"}, "outTotal": 2}}));
        let rust = reply(200, json!({"view": {"node": {"key": "R-2"}, "outTotal": 2}}));
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &rust, vec![])
                .verdict,
            Verdict::Same
        );
        let go = reply(200, json!({"view": {"node": {"props": {"a": [1, 2]}}}}));
        let rust = reply(200, json!({"view": {"node": {"props": {"a": [2, 1]}}}}));
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &rust, vec![])
                .verdict,
            Verdict::OrderDiffers
        );
    }

    #[test]
    fn errors_compare_by_code() {
        let mut c = Comparer::in_memory(&[]);
        let go = reply(404, json!({"code": "not_found", "message": "node R-1"}));
        let same = reply(404, json!({"code": "not_found", "message": "node R-1"}));
        let other = reply(404, json!({"code": "not_found", "message": "no node R-1"}));
        let unimpl = reply(501, json!({"code": "unimplemented", "message": "x"}));
        let ok = reply(200, json!({}));
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &same, vec![])
                .verdict,
            Verdict::Same
        );
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &other, vec![])
                .verdict,
            Verdict::MessageDiffers
        );
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &unimpl, vec![])
                .verdict,
            Verdict::RustUnimplemented
        );
        assert_eq!(
            c.judge(&proc(), &Bytes::new(), &json_headers(), &go, &ok, vec![])
                .verdict,
            Verdict::ErrorDiffers
        );
    }
}
