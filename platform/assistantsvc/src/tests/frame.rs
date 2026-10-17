//! frame_test.go

use goap_access::personal_unit;
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT, Change, ChangeID,
};
use goap_llm as llm;
use serde_json::Value;

use super::agents::{NEW_CHANGE_ARG, propose_args};
use super::*;
use crate::agents::{TOOL_LIST_AGENTS, TOOL_START_AGENT};
use crate::frame::{Frame, Level, searched_in};
use crate::methodtool::TOOL_METHODOLOGY_QUERY;
use crate::relate::{TOOL_ATTACH_CHANGE, TOOL_DETACH_CHANGE};
use crate::searchtool::{ACTION_SEARCH_CHANGES, TOOL_SEARCH_CHANGES, search_score, search_words};
use crate::turn::{
    TOOL_CREATE_CHANGE, TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE, TOOL_SELECT_PROJECT,
};

fn tool_names(specs: &[llm::ToolSpec]) -> Vec<String> {
    specs.iter().map(|s| s.name.clone()).collect()
}

fn has_new_change(specs: &[llm::ToolSpec]) -> bool {
    specs
        .iter()
        .find(|s| s.name == TOOL_START_AGENT)
        .and_then(|s| s.input_schema.get("properties"))
        .and_then(Value::as_object)
        .is_some_and(|p| p.contains_key("newChange"))
}

/// At level project the tools are those of a project; create_change and a new change on start_agent wait for a
/// search.
#[tokio::test]
async fn create_change_waits_for_a_search() {
    let e = new_env(&[
        &call(
            TOOL_CREATE_CHANGE,
            r#"{"title":"Ship v2","intent":"Customers need it"}"#,
        ),
        &call(TOOL_START_AGENT, &propose_args(NEW_CHANGE_ARG)),
        r#"{"message":"Search first."}"#,
    ])
    .await;
    e.send("start a change", in_project("PROJ-A"))
        .await
        .unwrap();
    let want = [
        TOOL_SELECT_PROJECT,
        TOOL_OPEN_CHANGE,
        TOOL_LIST_METHODOLOGIES,
        TOOL_METHODOLOGY_QUERY,
        TOOL_SEARCH_CHANGES,
        TOOL_LIST_AGENTS,
        TOOL_START_AGENT,
    ];
    let got = e.got();
    assert!(
        tool_names(&got[0].tools) == want && !has_new_change(&got[0].tools),
        "tools {:?} (newChange {})",
        tool_names(&got[0].tools),
        has_new_change(&got[0].tools)
    );
    let res = e.tool_result(1);
    assert!(
        res.contains("not available yet")
            && res.contains("search_changes")
            && res.contains("list_agents"),
        "create_change: {res}"
    );
    let res = e.tool_result(2);
    assert!(
        res.contains("only once the open changes were searched"),
        "start_agent: {res}"
    );
    let sys = &got[0].system;
    assert!(
        sys.contains("level: project")
            && sys.contains("project: PROJ-A")
            && sys.contains("server tools of this turn: select_project")
            && sys.contains("search the open changes (search_changes) on the intent"),
        "system {sys}"
    );
    assert!(
        e.graph.created().is_empty() && e.answer().await.actions.is_empty(),
        "something was created or proposed"
    );
}

/// A search in the turn offers create_change to the next rounds of the same turn.
#[tokio::test]
async fn create_change_after_a_search_in_the_turn() {
    let e = new_env(&[
        &call(TOOL_SEARCH_CHANGES, r#"{"q":"ship version two"}"#),
        &call(
            TOOL_CREATE_CHANGE,
            r#"{"title":"Ship v2","intent":"Customers need it"}"#,
        ),
        r#"{"message":"Created."}"#,
    ])
    .await;
    e.send("start a change", in_project("PROJ-A"))
        .await
        .unwrap();
    let got = e.got();
    assert!(
        !tool_names(&got[0].tools).contains(&TOOL_CREATE_CHANGE.to_string()),
        "create_change offered before the search"
    );
    assert!(
        tool_names(&got[1].tools).contains(&TOOL_CREATE_CHANGE.to_string())
            && has_new_change(&got[1].tools),
        "round 2 tools {:?}",
        tool_names(&got[1].tools)
    );
    assert_eq!(e.graph.created().len(), 1, "{}", e.tool_result(2));
    let a = e.answer().await;
    assert!(
        a.actions.len() == 2
            && s(&a.actions[0], "type") == ACTION_SEARCH_CHANGES
            && s(&a.actions[1], "type") == TOOL_CREATE_CHANGE,
        "actions {:?}",
        a.actions
    );
    // the next turn knows from the history that it searched
    e.set_answers(&[r#"{"message":"ok"}"#]);
    e.send("and another", in_project("PROJ-A")).await.unwrap();
    let got = e.got();
    assert!(
        tool_names(&got[0].tools).contains(&TOOL_CREATE_CHANGE.to_string()),
        "the search of an earlier turn is forgotten"
    );
    let last = &got[0].messages;
    let h = &last[last.len() - 2].content;
    assert!(
        h.contains(r#"searched the open changes for "ship version two": 0 found"#),
        "history {h:?}"
    );
}

/// A search of an earlier turn, read from the history, offers create_change at once.
#[tokio::test]
async fn create_change_after_a_search_in_the_history() {
    let e = new_env(&[
        &call(
            TOOL_CREATE_CHANGE,
            r#"{"title":"Ship v2","intent":"Customers need it"}"#,
        ),
        r#"{"message":"Created."}"#,
    ])
    .await;
    e.searched().await;
    e.send("create it", in_project("PROJ-A")).await.unwrap();
    let got = e.got();
    assert!(
        tool_names(&got[0].tools).contains(&TOOL_CREATE_CHANGE.to_string())
            && e.graph.created().len() == 1,
        "tools {:?}",
        tool_names(&got[0].tools)
    );
    assert!(
        !got[0]
            .system
            .contains("are offered once the open changes were searched"),
        "the frame still says to search"
    );
}

/// On a change the tools are those of a change, whatever was searched; the frame names the change and its
/// methodology.
#[tokio::test]
async fn tools_follow_the_frame_on_a_change() {
    let e = new_env(&[
        &call(TOOL_LIST_METHODOLOGIES, "{}"),
        &call(TOOL_CREATE_CHANGE, r#"{"title":"t","intent":"i"}"#),
        r#"{"message":"ok"}"#,
    ])
    .await;
    e.searched().await;
    e.send("what now?", change_ctx("CHG-D")).await.unwrap();
    let want = [
        TOOL_SELECT_PROJECT,
        TOOL_OPEN_CHANGE,
        TOOL_METHODOLOGY_QUERY,
        TOOL_LIST_AGENTS,
        TOOL_START_AGENT,
        TOOL_SEARCH_CHANGES,
        TOOL_ATTACH_CHANGE,
        TOOL_DETACH_CHANGE,
    ];
    let got = e.got();
    assert!(
        tool_names(&got[0].tools) == want && !has_new_change(&got[0].tools),
        "tools {:?}",
        tool_names(&got[0].tools)
    );
    for (round, tool) in [(1, TOOL_LIST_METHODOLOGIES), (2, TOOL_CREATE_CHANGE)] {
        let res = e.tool_result(round);
        assert!(
            res.contains("not available at level change")
                && res.contains(tool)
                && res.contains("search_changes"),
            "{tool}: {res}"
        );
    }
    let sys = &got[0].system;
    assert!(
        sys.contains("level: change")
            && sys.contains("change: CHG-D")
            && sys.contains("methodology: delivery"),
        "system {sys}"
    );
    assert!(e.graph.created().is_empty(), "created on a change");
    // the same person on the project: the tools of a project
    e.set_answers(&[r#"{"message":"ok"}"#]);
    e.send("and here?", in_project("PROJ-A")).await.unwrap();
    let project = Frame {
        level: Level::Project,
        ..Default::default()
    };
    assert_eq!(tool_names(&e.got()[0].tools), project.tools(true));
}

/// A change of the context the caller cannot read is no frame: the turn stays at the project, with a note.
#[tokio::test]
async fn unreadable_change_leaves_the_project_frame() {
    let e = new_env(&[r#"{"message":"ok"}"#]).await;
    e.send("hi", change_ctx("CHG-HERS")).await.unwrap();
    let got = e.got();
    assert!(
        tool_names(&got[0].tools).contains(&TOOL_LIST_METHODOLOGIES.to_string()),
        "tools {:?}",
        tool_names(&got[0].tools)
    );
    let sys = &got[0].system;
    assert!(
        sys.contains("level: project") && sys.contains("note: change \"CHG-HERS\" does not exist"),
        "system {sys}"
    );
}

fn hit(id: &str, title: &str, project: &str, status: goap_domain::ChangeStatus) -> Change {
    Change {
        id: ChangeID::from(id),
        title: title.into(),
        project_id: project.into(),
        status,
        ..Default::default()
    }
}

async fn search_env(arguments: &str) -> (Env, Value) {
    let e = new_env(&[&call(TOOL_SEARCH_CHANGES, arguments), r#"{"message":"ok"}"#]).await;
    {
        let mut p = e.projects.0.lock();
        p.access.get_mut("u1").unwrap().push(sv("PROJ-A1"));
        p.subs = Some(
            [(sv("PROJ-A"), vec![sv("PROJ-A1"), sv("PROJ-A2")])]
                .into_iter()
                .collect(),
        );
    }
    let long = "payment ".repeat(60);
    {
        let mut g = e.graph.0.lock();
        g.changes.clear();
        let mut c1 = hit("C1", "Payment gateway upgrade", "PROJ-A", CHANGE_DRAFT);
        c1.intent = format!("Upgrade the payment provider. {long}");
        c1.methodology = "delivery".into();
        c1.state = "design".into();
        let mut c2 = hit("C2", "Fix login", "PROJ-A", CHANGE_ACTIVE);
        c2.intent = "the payment page login fails".into();
        let mut c7 = hit("C7", "Payment upgrade", "PROJ-A", CHANGE_DRAFT);
        c7.owner_org = personal_unit("u2");
        let mut c8 = hit("C8", "Payment upgrade notes", "PROJ-A", CHANGE_DRAFT);
        c8.owner_org = personal_unit("u1");
        let mut c9 = hit("C9", "Unrelated", "PROJ-A", CHANGE_DRAFT);
        c9.intent = "nothing to see".into();
        for c in [
            c1,
            c2,
            hit("C3", "Payment upgrade", "PROJ-A", CHANGE_APPLIED),
            hit("C4", "Payment upgrade", "PROJ-B", CHANGE_ACTIVE),
            hit("C5", "Upgrade docs", "PROJ-A1", CHANGE_COMMITTED),
            hit("C6", "Payment", "PROJ-A2", CHANGE_ACTIVE),
            c7,
            c8,
            c9,
        ] {
            g.changes.insert(c.id.as_str().to_string(), c);
        }
    }
    e.send("find it", in_project("PROJ-A")).await.unwrap();
    let r = outcomes(&e.tool_result(1))[0].clone();
    (e, r)
}

fn err_of(r: &Value) -> &str {
    r["error"].as_str().unwrap_or("")
}

#[tokio::test]
async fn search_changes_ranks_and_filters() {
    // "to" and "a" are too short to count
    let (e, r) = search_env(r#"{"q":"Payment, upgrade to a"}"#).await;
    let changes = r["result"]["changes"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    let ids: Vec<&str> = changes
        .iter()
        .map(|h| h["changeId"].as_str().unwrap())
        .collect();
    // C1: 2+2 title, 1+1 intent; C8 (the caller's own personal change): 2+2; C5 (a sub-project): 2; C2: 1. Not C3
    // (applied), C4 (another project), C6 (a sub-project the caller may not use), C7 (personal to someone else), C9
    // (no word).
    assert!(
        ids == ["C1", "C8", "C5", "C2"] && r["result"]["total"] == 4 && err_of(&r).is_empty(),
        "{ids:?} {r}"
    );
    assert_eq!(
        r["result"]["projects"],
        serde_json::json!(["PROJ-A", "PROJ-A1"])
    );
    let c1 = &changes[0];
    assert!(
        c1["score"] == 6
            && c1["status"] == "draft"
            && c1["state"] == "design"
            && c1["methodology"] == "delivery"
            && c1["project"] == "PROJ-A"
            && c1["intent"].as_str().unwrap().len() <= 200 + "…".len(),
        "hit {c1}"
    );
    let a = e.answer().await;
    assert!(
        a.actions.len() == 1
            && s(&a.actions[0], "type") == ACTION_SEARCH_CHANGES
            && obj(&a.actions[0], "result")["count"] == 4,
        "actions {:?}",
        a.actions
    );
    assert!(
        searched_in(std::slice::from_ref(&a)),
        "the search is not read back from the message"
    );
}

#[tokio::test]
async fn search_changes_statuses_and_refusals() {
    let (_, r) = search_env(r#"{"q":"payment upgrade","status":["applied"]}"#).await;
    let changes = r["result"]["changes"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    assert!(changes.len() == 1 && changes[0]["changeId"] == "C3", "{r}");
    for (arguments, want) in [
        (r#"{"q":"a b"}"#, "at least one word"),
        (r#"{"q":"payment","status":"gone"}"#, "unknown status"),
        (r#"{"q":"payment","colour":"red"}"#, "unknown argument"),
    ] {
        let (_, r) = search_env(arguments).await;
        assert!(err_of(&r).contains(want), "{arguments}: {r}");
    }
}

#[test]
fn search_words_and_score() {
    assert_eq!(
        search_words("Fix the LOGIN, fix it; ok"),
        ["fix", "the", "login"]
    );
    let c = Change {
        title: "Login".into(),
        intent: "the login page".into(),
        ..Default::default()
    };
    assert_eq!(search_score(&[sv("login"), sv("page")], &c), 4);
}
