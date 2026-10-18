//! Command goap-eval runs offline evaluation cases (goap-eval, ADR 0099) against the model gateway, as the user of
//! the token given: every call goes through the gateway's admission and ledger (source "eval").
//!
//! ```text
//! goap-eval [-url http://localhost:8080] [-token $GOAP_TOKEN] [-model alias] [-runs n] [-json report.json] [-v] <file or dir>...
//! ```
//!
//! The exit status is 1 when a case does not reach its pass rate, 2 on a usage or loading error.

use std::io::Write;
use std::sync::Arc;
use std::time::Duration;

use goap_ctx::Ctx;
use goap_eval::{Report, Runner};

#[tokio::main]
async fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let code = run(&args, &mut std::io::stdout(), &mut std::io::stderr()).await;
    std::process::exit(code);
}

/// The flags of the command (Go's `flag` set).
#[derive(Debug, PartialEq)]
struct Flags {
    url: String,
    token: String,
    model: String,
    runs: i64,
    json: String,
    verbose: bool,
    timeout: Duration,
    paths: Vec<String>,
}

const USAGE: &str = "  -json string\n    \twrite the full report as JSON to this file\n  -model string\n    \talias or provider/model overriding the model of every case\n  -runs int\n    \truns of every case, overriding theirs\n  -timeout duration\n    \tbound of the whole evaluation (default 30m0s)\n  -token string\n    \tbearer token of the user the calls are made as (GOAP_TOKEN)\n  -url string\n    \tbase URL of the model gateway (goap-dev, the gateway or modelgw) (default \"http://localhost:8080\")\n  -v\tprint every check and the answers of failed runs\n";

/// Parses the arguments the way Go's `flag` package does: `-name value`, `-name=value`, `--name`, a boolean flag
/// alone or `-v=false`, flags stop at the first argument that is not one (or after `--`).
fn parse(args: &[String], env: impl Fn(&str) -> Option<String>) -> Result<Flags, String> {
    let mut f = Flags {
        url: env("GOAP_EVAL_URL")
            .filter(|v| !v.is_empty())
            .unwrap_or_else(|| "http://localhost:8080".into()),
        token: env("GOAP_TOKEN").unwrap_or_default(),
        model: String::new(),
        runs: 0,
        json: String::new(),
        verbose: false,
        timeout: Duration::from_secs(30 * 60),
        paths: Vec::new(),
    };
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        if a == "--" {
            i += 1;
            break;
        }
        if !a.starts_with('-') || a == "-" {
            break;
        }
        let body = a.trim_start_matches('-');
        if a.len() - body.len() > 2 || body.is_empty() || body.starts_with('=') {
            return Err(format!("bad flag syntax: {a}"));
        }
        let (name, inline) = match body.split_once('=') {
            Some((n, v)) => (n, Some(v.to_string())),
            None => (body, None),
        };
        i += 1;
        if name == "v" {
            f.verbose = match inline.as_deref() {
                None => true,
                Some(v) => parse_bool(v)
                    .ok_or_else(|| format!("invalid boolean value {v:?} for -v: parse error"))?,
            };
            continue;
        }
        if name == "h" || name == "help" {
            return Err(String::new());
        }
        let value = match inline {
            Some(v) => v,
            None => {
                let Some(v) = args.get(i) else {
                    return Err(format!("flag needs an argument: -{name}"));
                };
                i += 1;
                v.clone()
            }
        };
        match name {
            "url" => f.url = value,
            "token" => f.token = value,
            "model" => f.model = value,
            "json" => f.json = value,
            "runs" => {
                f.runs = value
                    .parse()
                    .map_err(|_| format!("invalid value {value:?} for flag -runs: parse error"))?
            }
            "timeout" => {
                f.timeout = parse_duration(&value).ok_or_else(|| {
                    format!("invalid value {value:?} for flag -timeout: parse error")
                })?
            }
            _ => return Err(format!("flag provided but not defined: -{name}")),
        }
    }
    f.paths = args[i..].to_vec();
    Ok(f)
}

fn parse_bool(v: &str) -> Option<bool> {
    match v {
        "1" | "t" | "T" | "true" | "TRUE" | "True" => Some(true),
        "0" | "f" | "F" | "false" | "FALSE" | "False" => Some(false),
        _ => None,
    }
}

/// Parses a Go duration ("30m", "1h30m", "1.5s", "250ms").
fn parse_duration(s: &str) -> Option<Duration> {
    if s == "0" {
        return Some(Duration::ZERO);
    }
    let mut rest = s;
    let mut total = 0f64;
    if rest.is_empty() {
        return None;
    }
    while !rest.is_empty() {
        let n = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        if n == 0 {
            return None;
        }
        let num: f64 = rest[..n].parse().ok()?;
        rest = &rest[n..];
        let u = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let unit = match &rest[..u] {
            "ns" => 1e-9,
            "us" | "µs" | "μs" => 1e-6,
            "ms" => 1e-3,
            "s" => 1.0,
            "m" => 60.0,
            "h" => 3600.0,
            _ => return None,
        };
        rest = &rest[u..];
        total += num * unit;
    }
    Some(Duration::from_secs_f64(total))
}

async fn run(args: &[String], stdout: &mut dyn Write, stderr: &mut dyn Write) -> i32 {
    let f = match parse(args, |k| std::env::var(k).ok()) {
        Ok(f) => f,
        Err(e) => {
            if !e.is_empty() {
                let _ = writeln!(stderr, "{e}");
            }
            let _ = write!(stderr, "Usage of goap-eval:\n{USAGE}");
            return 2;
        }
    };
    if f.paths.is_empty() {
        let _ = writeln!(
            stderr,
            "usage: goap-eval [flags] <case file or directory>..."
        );
        let _ = write!(stderr, "{USAGE}");
        return 2;
    }
    let cases = match goap_eval::load(&f.paths) {
        Ok(c) => c,
        Err(e) => {
            let _ = writeln!(stderr, "goap-eval: {e}");
            return 2;
        }
    };
    let http = reqwest::Client::builder()
        .timeout(Duration::from_secs(10 * 60))
        .build()
        .unwrap_or_default();
    let client = gateway(http, f.url.trim_end_matches('/'), &f.token);
    let (ctx, _cancel) = Ctx::background().with_timeout(f.timeout);
    let mut runner = Runner::new(Arc::new(client));
    runner.model = f.model;
    runner.runs = f.runs;
    let rep = runner.run_all(&ctx, &cases).await;
    print(stdout, &rep, f.verbose);
    if !f.json.is_empty() {
        let b = serde_json::to_vec_pretty(&rep).unwrap_or_default();
        if let Err(e) = std::fs::write(&f.json, b) {
            let _ = writeln!(stderr, "goap-eval: {e}");
            return 2;
        }
    }
    if rep.failed > 0 {
        return 1;
    }
    0
}

/// The model gateway at `base_url` as an `llm::Client`, sending `token` (when not empty) as a bearer token (Go's
/// `modelgw.NewClient` over a client adding the `Authorization` header).
fn gateway(http: reqwest::Client, base_url: &str, token: &str) -> goap_modelgw::Client {
    let mut b = goap_connect::Client::builder(base_url).http(http);
    if !token.is_empty() {
        b = b.header("authorization", &format!("Bearer {token}"));
    }
    goap_modelgw::Client::new(b.build())
}

/// Writes the rows of `rows` aligned the way `text/tabwriter` (padding 2, spaces) does: every cell but the last of
/// a line is padded to the widest of its column plus two.
fn tabulate(w: &mut dyn Write, rows: &[Vec<String>]) {
    let cols = rows.iter().map(Vec::len).max().unwrap_or(0);
    let mut widths = vec![0usize; cols];
    for r in rows {
        for (i, c) in r.iter().enumerate().take(r.len().saturating_sub(1)) {
            widths[i] = widths[i].max(c.chars().count());
        }
    }
    for r in rows {
        let mut line = String::new();
        for (i, c) in r.iter().enumerate() {
            line.push_str(c);
            if i + 1 < r.len() {
                let pad = widths[i] + 2 - c.chars().count();
                line.extend(std::iter::repeat_n(' ', pad));
            }
        }
        let _ = writeln!(w, "{line}");
    }
}

fn print(w: &mut dyn Write, rep: &Report, verbose: bool) {
    let mut rows = vec![
        [
            "CASE", "MODEL", "PASS", "RATE", "IN TOK", "OUT TOK", "TOOLS", "MS",
        ]
        .map(String::from)
        .to_vec(),
    ];
    for c in &rep.cases {
        let mark = if c.passed { "ok" } else { "FAIL" };
        rows.push(vec![
            c.name.clone(),
            c.model.clone(),
            mark.into(),
            format!("{:.0}%", 100.0 * c.pass_rate),
            format!("{:.0}", c.input_tokens),
            format!("{:.0}", c.output_tokens),
            format!("{:.1}", c.tool_calls),
            format!("{:.0}", c.duration_ms),
        ]);
    }
    tabulate(w, &rows);
    for c in &rep.cases {
        for (i, r) in c.runs.iter().enumerate() {
            if r.passed && !verbose {
                continue;
            }
            let _ = writeln!(
                w,
                "\n{}, run {}: passed={} trajectory=[{}]",
                c.name,
                i + 1,
                r.passed,
                r.trajectory.join(" ")
            );
            for ch in &r.checks {
                if !ch.passed || verbose {
                    let _ = writeln!(w, "  [{}] {} {}", mark(ch.passed), ch.name, ch.detail);
                }
            }
            if !r.error.is_empty() {
                let _ = writeln!(w, "  error: {}", r.error);
            }
            if !r.passed {
                let answer: String = r.answer.chars().take(600).collect();
                let _ = writeln!(w, "  answer: {answer}");
            }
        }
    }
    let _ = writeln!(w, "\n{} passed, {} failed", rep.passed, rep.failed);
}

fn mark(ok: bool) -> &'static str {
    if ok { "ok" } else { "x" }
}

#[cfg(test)]
mod tests;
