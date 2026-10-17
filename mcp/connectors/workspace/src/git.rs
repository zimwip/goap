//! The sources of a workspace: clone, status, diff, push (`git.go`).

use std::process::Stdio;
use std::time::Duration;

use base64::Engine as _;
use goap_connect::Ctx;
use goap_connectorkit::InvokeError;
use serde_json::{Map, Value, json};
use tokio::process::Command;

use crate::shell::{cap_output, environment};
use crate::workspace::{SECRET_GIT_TOKEN, Space, branch_of, rel, string_arg, to_map};

/// baseKey is the git configuration of a source holding the commit the change started from.
const BASE_KEY: &str = "goap.base";

/// gitTimeout bounds a git operation of the connector (a clone of a large repository included).
const GIT_TIMEOUT: Duration = Duration::from_secs(10 * 60);

/// git runs git in dir with the environment of the workspace and, when the unit gives one, the token of the
/// remote, passed as configuration in the environment (never on the command line, never to the commands).
pub(crate) async fn git(
    ctx: &Ctx,
    s: &Space,
    dir: &std::path::Path,
    auth: bool,
    args: &[&str],
) -> Result<String, InvokeError> {
    let mut cmd = Command::new("git");
    cmd.args(args)
        .current_dir(dir)
        .env_clear()
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    for kv in environment(s) {
        if let Some((k, v)) = kv.split_once('=') {
            cmd.env(k, v);
        }
    }
    if let Some(token) = s.secrets.get(SECRET_GIT_TOKEN)
        && auth
        && !token.is_empty()
    {
        let basic =
            base64::engine::general_purpose::STANDARD.encode(format!("x-access-token:{token}"));
        cmd.env("GIT_CONFIG_COUNT", "1")
            .env("GIT_CONFIG_KEY_0", "http.extraHeader")
            .env(
                "GIT_CONFIG_VALUE_0",
                format!("Authorization: Basic {basic}"),
            );
    }
    let fail = |msg: String| -> InvokeError { format!("git {}: {msg}", args[0]).into() };
    let child = cmd.spawn().map_err(|e| fail(e.to_string()))?;
    let out = tokio::select! {
        out = tokio::time::timeout(GIT_TIMEOUT, child.wait_with_output()) => match out {
            Ok(out) => out.map_err(|e| fail(e.to_string()))?,
            Err(_) => return Err(fail("signal: killed".into())),
        },
        _ = ctx.done() => return Err(fail("signal: killed".into())),
    };
    if !out.status.success() {
        let mut msg = String::from_utf8_lossy(&out.stderr).trim().to_string();
        if msg.is_empty() {
            msg = match out.status.code() {
                Some(c) => format!("exit status {c}"),
                None => "signal: killed".into(),
            };
        }
        return Err(fail(msg));
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

/// repoName is the directory a repository is cloned in by default: the last element of its URL or path.
pub(crate) fn repo_name(repo: &str) -> String {
    let repo = repo.replace(std::path::MAIN_SEPARATOR, "/");
    let repo = repo.trim_end_matches('/');
    let repo = match repo.rfind(['/', ':']) {
        Some(i) => &repo[i + 1..],
        None => repo,
    };
    repo.strip_suffix(".git").unwrap_or(repo).to_string()
}

fn is_git_dir(dir: &std::path::Path) -> bool {
    std::fs::metadata(dir.join(".git")).is_ok()
}

pub(crate) async fn open(
    ctx: &Ctx,
    s: &Space,
    args: &Map<String, Value>,
) -> Result<Map<String, Value>, InvokeError> {
    let empty = Map::new();
    for item in args
        .get("sources")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
    {
        let src = item.as_object().unwrap_or(&empty);
        let repo = string_arg(src, "repo");
        if repo.is_empty() {
            return Err("a source needs a repo".into());
        }
        let mut p = rel(string_arg(src, "path"));
        if p == "." {
            p = rel(&repo_name(repo));
        }
        if p == "." || p.contains('/') {
            return Err(format!(
                "source {repo}: path {p:?} must name a directory at the top of the workspace"
            )
            .into());
        }
        let branch = match string_arg(src, "branch") {
            "" => branch_of(s),
            b => b.to_string(),
        };
        let dir = s.local(&p);
        if is_git_dir(&dir) {
            continue; // already cloned: opening again keeps the work
        }
        let mut clone = vec!["clone", "--quiet"];
        let r = string_arg(src, "ref");
        if !r.is_empty() {
            clone.extend(["--branch", r]);
        }
        clone.extend(["--", repo, &p]);
        git(ctx, s, &s.dir, true, &clone).await?;
        git(ctx, s, &dir, false, &["checkout", "--quiet", "-B", &branch]).await?;
        let head = git(ctx, s, &dir, false, &["rev-parse", "HEAD"]).await?;
        // the commit the change started from: what diff compares with, whatever the commits made since
        git(ctx, s, &dir, false, &["config", BASE_KEY, &head]).await?;
    }
    Ok(to_map(
        json!({"directory": s.dir_str(), "sources": sources(ctx, s).await}),
    ))
}

/// sources are the git repositories at the top of the workspace: their branch, head and whether they changed.
pub(crate) async fn sources(ctx: &Ctx, s: &Space) -> Vec<Value> {
    let Ok(entries) = std::fs::read_dir(&s.dir) else {
        return Vec::new();
    };
    let mut names: Vec<String> = entries
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    names.sort();
    let mut out = Vec::new();
    for name in names {
        let dir = s.dir.join(&name);
        if !is_git_dir(&dir) {
            continue;
        }
        let mut item = to_map(json!({"path": name}));
        if let Ok(b) = git(ctx, s, &dir, false, &["rev-parse", "--abbrev-ref", "HEAD"]).await {
            item.insert("branch".into(), json!(b));
        }
        if let Ok(h) = git(ctx, s, &dir, false, &["rev-parse", "HEAD"]).await {
            item.insert("head".into(), json!(h));
        }
        if let Ok(st) = git(ctx, s, &dir, false, &["status", "--porcelain"]).await {
            item.insert("changed".into(), json!(!st.is_empty()));
        }
        if let Ok(r) = git(ctx, s, &dir, false, &["remote", "get-url", "origin"]).await {
            item.insert("remote".into(), json!(r));
        }
        out.push(Value::Object(item));
    }
    out
}

/// diff gives, per source (or for the one named), what changed: the files (git status, new files included) and the
/// diff of the tracked ones against the commit the change started from, commits made since included.
pub(crate) async fn diff(
    ctx: &Ctx,
    s: &Space,
    args: &Map<String, Value>,
) -> Result<Map<String, Value>, InvokeError> {
    let p = rel(string_arg(args, "path"));
    let dirs: Vec<String> = if p != "." {
        vec![p]
    } else {
        sources(ctx, s)
            .await
            .iter()
            .filter_map(|src| src["path"].as_str().map(String::from))
            .collect()
    };
    let mut out = Vec::new();
    for p in dirs {
        let dir = s.local(&p);
        let status = git(ctx, s, &dir, false, &["status", "--porcelain"]).await?;
        let base = match git(ctx, s, &dir, false, &["config", "--get", BASE_KEY]).await {
            Ok(b) if !b.is_empty() => b,
            _ => "HEAD".to_string(),
        };
        let text = git(ctx, s, &dir, false, &["diff", &base]).await?;
        out.push(
            json!({"path": p, "base": base, "status": status, "diff": cap_output(text.as_bytes())}),
        );
    }
    Ok(to_map(json!({ "sources": out })))
}

/// push pushes the current branch of a source to its origin, only when it is the branch of the change: the commands
/// of the workspace do not get the token, so that what reaches the remote is the work of the change only.
pub(crate) async fn push(
    ctx: &Ctx,
    s: &Space,
    args: &Map<String, Value>,
) -> Result<Map<String, Value>, InvokeError> {
    let p = rel(string_arg(args, "path"));
    if p == "." {
        return Err("push needs the directory of a source".into());
    }
    let dir = s.local(&p);
    let branch = git(ctx, s, &dir, false, &["rev-parse", "--abbrev-ref", "HEAD"]).await?;
    let want = branch_of(s);
    if branch != want {
        return Err(format!(
            "source {p} is on {branch}: only the branch of the change ({want}) is pushed"
        )
        .into());
    }
    git(
        ctx,
        s,
        &dir,
        true,
        &["push", "--quiet", "--set-upstream", "origin", &branch],
    )
    .await?;
    let head = git(ctx, s, &dir, false, &["rev-parse", "HEAD"])
        .await
        .unwrap_or_default();
    let remote = git(ctx, s, &dir, false, &["remote", "get-url", "origin"])
        .await
        .unwrap_or_default();
    Ok(to_map(
        json!({"path": p, "branch": branch, "head": head, "remote": remote}),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn repositories_name_their_directory() {
        assert_eq!(repo_name("https://example.com/a/app.git"), "app");
        assert_eq!(repo_name("git@github.com:org/app.git/"), "app");
        assert_eq!(repo_name("host:app"), "app");
        assert_eq!(repo_name("app"), "app");
    }
}
