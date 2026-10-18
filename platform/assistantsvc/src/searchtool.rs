//! search_changes (`searchtool.go`): the changes of the active project matching a text, what the assistant shows
//! before it proposes a new change.

use goap_access::{is_personal, is_personal_to};
use goap_authz::with_principal;
use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT, Change,
    ChangeStatus,
};
use goap_graphapi::ChangesFilter;
use serde::Serialize;
use serde_json::{Value, json};

use crate::turn::{Turn, action};
use crate::util::{Args, clip, q};

/// The tool listing the changes of the active project matching a text.
pub const TOOL_SEARCH_CHANGES: &str = "search_changes";

/// The action a search leaves on the assistant message: the record, read back from the history, that the open
/// changes were searched in the conversation (a label in the interface, never run).
pub const ACTION_SEARCH_CHANGES: &str = "search_changes";

const MAX_SEARCH_RESULTS: usize = 10;
const MIN_SEARCH_WORD: usize = 3;

/// The open changes: the ones a new piece of work may join.
fn default_search_statuses() -> Vec<ChangeStatus> {
    vec![CHANGE_DRAFT, CHANGE_ACTIVE, CHANGE_COMMITTED]
}

fn all_statuses() -> Vec<ChangeStatus> {
    vec![
        CHANGE_DRAFT,
        CHANGE_ACTIVE,
        CHANGE_COMMITTED,
        CHANGE_APPLIED,
        CHANGE_ABANDONED,
    ]
}

/// changeHit is a change as search_changes returns it.
#[derive(Debug, Clone, Serialize)]
struct ChangeHit {
    #[serde(rename = "changeId")]
    change_id: String,
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    intent: String,
    status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    state: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    methodology: String,
    project: String,
    score: i64,
}

/// searchWords are the distinct lower-case words of a query of at least minSearchWord letters or digits.
pub(crate) fn search_words(q: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for w in q
        .to_lowercase()
        .split(|r: char| !r.is_alphabetic() && !r.is_numeric())
        .filter(|w| !w.is_empty())
    {
        if w.chars().count() >= MIN_SEARCH_WORD && !out.iter().any(|o| o == w) {
            out.push(w.to_string());
        }
    }
    out
}

/// searchScore is the lexical score of a change for the words: per word, 2 when the title holds it, 1 when the intent
/// does (case-insensitive).
pub(crate) fn search_score(words: &[String], c: &Change) -> i64 {
    let (title, intent) = (c.title.to_lowercase(), c.intent.to_lowercase());
    let mut n = 0;
    for w in words {
        if title.contains(w.as_str()) {
            n += 2;
        }
        if intent.contains(w.as_str()) {
            n += 1;
        }
    }
    n
}

/// statusesOf reads the optional "status" argument: one status or a list; the open ones by default.
pub(crate) fn statuses_of(a: &Args) -> Result<Vec<ChangeStatus>, String> {
    let raw: Vec<Value> = match a.0.get("status") {
        None | Some(Value::Null) => return Ok(default_search_statuses()),
        Some(Value::String(v)) => {
            if v.trim().is_empty() {
                return Ok(default_search_statuses());
            }
            vec![Value::String(v.clone())]
        }
        Some(Value::Array(v)) => v.clone(),
        Some(_) => return Err(r#""status" is a status or a list of statuses"#.into()),
    };
    let all = all_statuses();
    let mut out: Vec<ChangeStatus> = Vec::new();
    for r in raw {
        let s = r.as_str().unwrap_or("");
        let st = ChangeStatus::new(s.trim().to_lowercase());
        if !all.contains(&st) {
            return Err(format!(
                "unknown status {}: the statuses are draft, active, committed, applied, abandoned",
                q(s)
            ));
        }
        if !out.contains(&st) {
            out.push(st);
        }
    }
    if out.is_empty() {
        return Ok(default_search_statuses());
    }
    Ok(out)
}

impl Turn {
    /// projectsToSearch are the projects of the search: the active one (the root when none is named) and, when the
    /// directory tells them, the ones below it the caller may work on.
    async fn projects_to_search(&self, ctx: &Ctx) -> Result<Vec<String>, String> {
        let mut project = self.project.clone();
        if project.is_empty() {
            project = self
                .s
                .projects
                .root_project(ctx)
                .await
                .map_err(|e| e.to_string())?;
        } else if !matches!(
            self.s
                .projects
                .may_access_project(ctx, &self.p, &project)
                .await,
            Ok(true)
        ) {
            return Err(format!("you may not work on project {}", q(&project)));
        }
        let mut out = vec![project.clone()];
        let Some(subs) = self.s.projects.sub_projects(ctx, &project).await else {
            return Ok(out);
        };
        let subs = subs.map_err(|e| format!("the organisation cannot be read: {e}"))?;
        for s in subs {
            if s == project || out.contains(&s) {
                continue;
            }
            if matches!(
                self.s.projects.may_access_project(ctx, &self.p, &s).await,
                Ok(true)
            ) {
                out.push(s);
            }
        }
        Ok(out)
    }

    /// searchChanges lists the changes of the active project chain matching q, as the caller, best first.
    pub(crate) async fn search_changes(&mut self, ctx: &Ctx, a: &Args) -> Result<Value, String> {
        for k in a.0.keys() {
            if k != "q" && k != "status" {
                return Err(format!(
                    "unknown argument {}: the arguments are q, status",
                    q(k)
                ));
            }
        }
        let query = a.str("q");
        let words = search_words(&query);
        if words.is_empty() {
            return Err(format!(
                r#""q" needs at least one word of {MIN_SEARCH_WORD} characters or more"#
            ));
        }
        let statuses = statuses_of(a)?;
        let projects = self.projects_to_search(ctx).await?;
        let cs = self
            .s
            .graph
            .list_changes(
                &with_principal(ctx, self.p.clone()),
                ChangesFilter {
                    status: statuses.clone(),
                    ..Default::default()
                },
            )
            .await
            .map_err(|e| format!("the changes cannot be listed: {e}"))?;
        let mut hits: Vec<ChangeHit> = Vec::new();
        for c in &cs {
            if !projects.contains(&c.project_id)
                || (is_personal(c) && !is_personal_to(c, &self.p.subject))
            {
                continue;
            }
            let score = search_score(&words, c);
            if score == 0 {
                continue;
            }
            hits.push(ChangeHit {
                change_id: c.id.as_str().to_string(),
                title: clip(&c.title, 200),
                intent: clip(&c.intent, 200),
                status: c.status.as_str().to_string(),
                state: c.state.clone(),
                methodology: c.methodology.clone(),
                project: c.project_id.clone(),
                score,
            });
        }
        hits.sort_by(|x, y| {
            y.score
                .cmp(&x.score)
                .then_with(|| x.change_id.cmp(&y.change_id))
        });
        let total = hits.len();
        hits.truncate(MAX_SEARCH_RESULTS);
        let st: Vec<&str> = statuses.iter().map(|s| s.as_str()).collect();
        let note = if total == 0 {
            "no change matches: tell the person, and create a change only if they want one"
        } else {
            "show the person what was found; propose to open one that fits, and create a change only if none fits or they say so"
        };
        let res = json!({"q": query, "projects": projects, "statuses": st, "total": total, "changes": hits, "note": note});
        self.searched = true;
        self.actions
            .push(action(json!({"type": ACTION_SEARCH_CHANGES,
            "label": clip(&format!("Searched the changes for {} ({total} found)", q(&query)), 200),
            "args": {"q": query}, "result": {"count": total}})));
        Ok(res)
    }
}
