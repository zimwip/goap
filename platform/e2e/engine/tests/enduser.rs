//! Go: enduser_test.go.

use std::sync::Arc;

use goap_authz::{self as authz, Principal};
use goap_ctx::Ctx;
use goap_domain::ChangeEvent;
use goap_engine::{
    LLMExecutor, STATUS_COMPLETED, StartRequest, TriggerEvent, TriggerManager, trigger_event_of,
};
use goap_llm as llm;
use goap_methodology as methodology;
use parking_lot::Mutex;

use crate::common::*;
use crate::companion::{RISKY_YAML, accompany, companion_of};
use crate::reaction::note_change;
use crate::steps::staged_setup;
use crate::triggers::{NOTE_ANSWER, NOTE_TRIGGER_YAML};

/// What a person's action sets off acts on their behalf, from one consequence to the next (ADR 0114): the run a
/// trigger starts on the event of their edit, its model calls, and the sub-agents it starts; the attribution never
/// grants anything (the run acts as the trigger).
#[tokio::test]
async fn triggered_run_acts_on_behalf_of_the_end_user() {
    let ctx = ctx();
    let mut s = staged_setup().await;
    add_yaml(&mut s, NOTE_TRIGGER_YAML);
    let callers: Arc<Mutex<Vec<Principal>>> = Arc::default();
    let c2 = callers.clone();
    s.e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(llm::ClientFunc::shared(
                move |ctx: Ctx, _r: llm::Request| {
                    let callers = c2.clone();
                    async move {
                        callers.lock().push(authz::principal_from(&ctx));
                        Ok(llm::Response {
                            text: NOTE_ANSWER.into(),
                            ..Default::default()
                        })
                    }
                },
            )),
        }),
    );
    let (e, g, _, _errs) = s.share();
    let tm = TriggerManager::new(e.clone());
    tm.reload(&ctx).await.unwrap();
    let ch = note_change(&g, "write a note", "noted").await;
    // alice edits the change: the event names her (ChangeEvent.end_user), the trigger's run acts for her
    let bb = g.blackboard(&ctx, &ch.id).await.unwrap();
    tm.handle(
        &ctx,
        trigger_event_of(ChangeEvent {
            r#type: "change.edited".into(),
            change: bb.change.clone(),
            end_user: "alice".into(),
            ..Default::default()
        }),
    )
    .await;
    e.drain().await;
    let run = e
        .store
        .list(&ctx)
        .await
        .unwrap()
        .into_iter()
        .rfind(|p| p.change_id == ch.id && !p.trigger.is_empty())
        .expect("the trigger ran");
    assert_eq!(run.status, STATUS_COMPLETED, "the trigger ran: {run:?}");
    assert!(
        run.initiator.subject == "system:trigger:noted/writer/on_edit"
            && run.initiator.on_behalf_of == "alice"
            && run.initiator.end_user() == "alice",
        "the run acts as the trigger, on behalf of alice: {:?}",
        run.initiator
    );
    {
        let callers = callers.lock();
        assert!(
            callers.len() == 1
                && callers[0].subject == run.initiator.subject
                && callers[0].end_user() == "alice",
            "its model call is attributed to alice: {callers:?}"
        );
    }

    // a sub-agent a platform service starts for the run inherits its end user
    let sub_req = || StartRequest {
        methodology: "noted".into(),
        goal: "noted".into(),
        change_id: ch.id.clone(),
        parent_id: run.id.clone(),
        ..Default::default()
    };
    let sub = e
        .start(&as_who(&authz::system("engine", &[])), sub_req())
        .await
        .unwrap();
    assert!(
        sub.initiator.subject == "system:engine" && sub.initiator.on_behalf_of == "alice",
        "the sub-agent acts on behalf of alice: {:?}",
        sub.initiator
    );
    // a person starting one acts for themselves
    let own = e
        .start(
            &as_who(&Principal {
                subject: "bob".into(),
                on_behalf_of: "alice".into(),
                ..Default::default()
            }),
            sub_req(),
        )
        .await
        .unwrap();
    assert_eq!(
        own.initiator.end_user(),
        "bob",
        "a person is their own end user"
    );
}

/// A companion run (ADR 0036 §3) set off by a person's run acts on their behalf.
#[tokio::test]
async fn companion_acts_on_behalf_of_the_end_user() {
    let mut s = staged_setup().await;
    add_yaml(&mut s, RISKY_YAML);
    let (e, _, _, _errs) = s.share();
    let p = e
        .start(
            &as_who(&Principal {
                subject: "alice".into(),
                ..Default::default()
            }),
            StartRequest {
                methodology: "staged".into(),
                goal: "delivery".into(),
                intent: "deliver the note".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    accompany(
        &e,
        TriggerEvent {
            r#type: "process.attached".into(),
            process: Some(p.clone()),
            ..Default::default()
        },
    )
    .await;
    let cs = companion_of(&e, &p.change_id).await;
    assert!(
        cs.len() == 1 && cs[0].initiator.is_system() && cs[0].initiator.on_behalf_of == "alice",
        "the companion acts on behalf of alice: {cs:?}"
    );
}
