//! The configuration of the proxy, read from the environment.
use std::{path::PathBuf, str::FromStr};

use anyhow::{Context, bail};
use http::Uri;

/// What the proxy does with a call.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// Every call to the Go graph service.
    Go,
    /// Every call to the Rust graph service.
    Rust,
    /// Every call to the Rust graph service first, replayed on the Go one when it answers `unimplemented`: both on
    /// one database.
    Split,
    /// Every call to the Go graph service, which answers, and again to the Rust one, each on its own database: the
    /// two answers are compared call by call.
    Compare,
    /// Every call to the Go service, which answers; a call that only reads is sent again to the Rust one, both on one
    /// database, and the two answers are compared: the reads of the Rust service checked against what Go wrote.
    Shadow,
}

impl Mode {
    pub fn name(self) -> &'static str {
        match self {
            Mode::Go => "go",
            Mode::Rust => "rust",
            Mode::Split => "split",
            Mode::Compare => "compare",
            Mode::Shadow => "shadow",
        }
    }
}

impl FromStr for Mode {
    type Err = anyhow::Error;

    fn from_str(s: &str) -> anyhow::Result<Mode> {
        Ok(match s {
            "go" => Mode::Go,
            "rust" => Mode::Rust,
            "split" => Mode::Split,
            "compare" => Mode::Compare,
            "shadow" => Mode::Shadow,
            _ => bail!("GOAP_PROXY_MODE: {s:?} is none of go, rust, split, compare, shadow"),
        })
    }
}

/// The services of the graph process: the ones `/proxy/coverage` and `/proxy/probe` look at.
pub const GRAPH_SERVICES: &[&str] = &[
    "goap.graph.v1.GraphService",
    "goap.registry.v1.DomainService",
    "goap.smarttag.v1.SmartTagService",
    "goap.importer.v1.ImportService",
];

#[derive(Debug, Clone)]
pub struct Config {
    pub mode: Mode,
    /// `GOAP_HTTP_ADDR`, default `:8080`.
    pub addr: String,
    /// `GOAP_PROXY_GO`: the Go graph service.
    pub go: Option<Uri>,
    /// `GOAP_PROXY_RUST`: the Rust graph service (or a proxy in mode split in front of both, for a comparison during
    /// the transition).
    pub rust: Option<Uri>,
    /// `GOAP_PROXY_REPORT_DIR`: where a comparison writes its calls, its summary and its pairs of ids.
    pub report_dir: PathBuf,
    /// `GOAP_PROXY_IGNORE`: the fields a comparison leaves out, comma separated, `[<Method>:]<path>`.
    pub ignore: Vec<String>,
    /// `GOAP_PROXY_MAX_BODY`: the largest body read, in bytes (default 256 MiB).
    pub max_body: usize,
}

fn uri(name: &str) -> anyhow::Result<Option<Uri>> {
    match std::env::var(name) {
        Ok(s) if !s.trim().is_empty() => {
            let u: Uri = s
                .trim()
                .trim_end_matches('/')
                .parse()
                .with_context(|| format!("{name}: {s:?}"))?;
            if u.scheme_str() != Some("http") {
                bail!("{name}: {s:?} is no http:// URL (the proxy speaks cleartext HTTP to the services)");
            }
            Ok(Some(u))
        }
        _ => Ok(None),
    }
}

impl Config {
    pub fn from_env() -> anyhow::Result<Config> {
        let mode: Mode = std::env::var("GOAP_PROXY_MODE")
            .unwrap_or_else(|_| "go".into())
            .parse()?;
        let mut addr = std::env::var("GOAP_HTTP_ADDR").unwrap_or_else(|_| ":8080".into());
        if addr.starts_with(':') {
            addr = format!("0.0.0.0{addr}");
        }
        let cfg = Config {
            mode,
            addr,
            go: uri("GOAP_PROXY_GO")?,
            rust: uri("GOAP_PROXY_RUST")?,
            report_dir: std::env::var("GOAP_PROXY_REPORT_DIR")
                .unwrap_or_else(|_| ".goap/perf/diff".into())
                .into(),
            ignore: std::env::var("GOAP_PROXY_IGNORE")
                .unwrap_or_default()
                .split(',')
                .map(str::trim)
                .filter(|s| !s.is_empty())
                .map(String::from)
                .collect(),
            max_body: match std::env::var("GOAP_PROXY_MAX_BODY") {
                Ok(s) => s.parse().context("GOAP_PROXY_MAX_BODY")?,
                Err(_) => 256 << 20,
            },
        };
        let need_go = matches!(mode, Mode::Go | Mode::Split | Mode::Compare | Mode::Shadow);
        let need_rust = matches!(mode, Mode::Rust | Mode::Split | Mode::Compare | Mode::Shadow);
        if need_go && cfg.go.is_none() {
            bail!("mode {}: GOAP_PROXY_GO is not set", mode.name());
        }
        if need_rust && cfg.rust.is_none() {
            bail!("mode {}: GOAP_PROXY_RUST is not set", mode.name());
        }
        Ok(cfg)
    }
}
