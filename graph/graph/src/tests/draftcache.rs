//! Port of `draftcache_test.go`: the drafts the graph reads (the cache extended event by event) are a from-scratch
//! fold of the log (ADR 0079 §2). (Go's `BenchmarkDraftRead` has no counterpart: the crate has no bench harness.)

use goap_domain::*;
use serde_json::json;

use crate::Graph;
use crate::changelog::impact_events;
use crate::tests::draft::{own_change, title};
use crate::tests::helpers::*;
use crate::testsupport::*;

fn norm_drafts(l: &[Draft]) -> String {
    if l.is_empty() {
        return "[]".into();
    }
    serde_json::to_string(l).expect("json")
}

/// scratchDrafts is the drafts of a change as one comparable text, from a from-scratch fold of its whole impact log.
async fn scratch_drafts(g: &Graph, id: &ChangeID) -> String {
    let ctx = ctx();
    let mut tx = must(g.begin(&ctx).await);
    let events = must(impact_events(&ctx, &mut tx, id).await);
    norm_drafts(&fold_drafts(&events))
}

async fn read_drafts(g: &Graph, id: &ChangeID) -> String {
    let ctx = ctx();
    let mut tx = must(g.begin(&ctx).await);
    norm_drafts(&must(g.drafts(&ctx, &mut tx, id).await))
}

async fn edit_title(g: &Graph, id: &ChangeID, impact: &ChangeImpactID, t: &str) -> ChangeImpact {
    must(
        edit(
            g,
            id,
            impact,
            Edit {
                properties: title(t),
                ..Default::default()
            },
        )
        .await,
    )
}

async fn proposed(f: &Fixture, t: &str) -> (Change, ChangeImpact) {
    let g = &f.g;
    let c = must(g.create_change(&ctx(), own_change(&f.base, t)).await);
    let m = must(
        g.propose_impact(
            &ctx(),
            &c.id,
            vec![ChangeImpact {
                intent: INTENT_MODIFIED,
                pre: Some(f.req.r#ref()),
                rationale: "edit".into(),
                ..Default::default()
            }],
        )
        .await,
    );
    (c, m[0].clone())
}

// After any sequence of operations the drafts the graph reads are the ones a from-scratch fold of the log gives, and
// a read after new events folds only those.
#[tokio::test]
async fn draft_cache_equals_scratch_fold() {
    for_each_repo(async |r: &TestRepo| {
        let f = new_fixture(r).await;
        let g = &f.g;
        let (c, m) = proposed(&f, "cache").await;
        for i in 0..20u8 {
            edit_title(g, &c.id, &m.id, &format!("t{}", (b'a' + i) as char)).await;
            let (got, want) = (read_drafts(g, &c.id).await, scratch_drafts(g, &c.id).await);
            assert_eq!(
                got,
                want,
                "after {} edits the cached drafts differ from the fold",
                i + 1
            );
        }
        // a read with nothing new folds nothing
        let before = g.draft_states.folded();
        read_drafts(g, &c.id).await;
        assert_eq!(
            g.draft_states.folded() - before,
            0,
            "a read with no new event folded events"
        );
        // one more edit: only its events are folded, not the 20 before
        edit_title(g, &c.id, &m.id, "last").await;
        let before = g.draft_states.folded();
        let (got, want) = (read_drafts(g, &c.id).await, scratch_drafts(g, &c.id).await);
        assert_eq!(got, want, "the cached drafts differ from the fold");
        let n = g.draft_states.folded() - before;
        assert!(
            (1..=2).contains(&n),
            "a read after one edit folded {n} events, want the new one only"
        );
        // a cold graph on the same log folds everything and agrees
        let cold = r.graph();
        let (got, want) = (
            read_drafts(&cold, &c.id).await,
            scratch_drafts(g, &c.id).await,
        );
        assert_eq!(got, want, "cold read differs");
    })
    .await;
}

// A transaction that wrote events and rolls back leaves no trace in the cache; inside it, the writes are read;
// another graph on the same store sees the events of the first.
#[tokio::test]
async fn draft_cache_rollback_and_shared_log() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let other = r.graph(); // a second graph (another process) on the same store
        let (c, m) = proposed(&f, "rollback").await;
        let co = edit_title(g, &c.id, &m.id, "kept").await;
        read_drafts(g, &c.id).await; // warm the cache
        read_drafts(&other, &c.id).await;

        {
            let mut tx = must(g.begin(&ctx).await);
            must(
                g.emit(
                    &ctx,
                    &mut tx,
                    vec![ImpactEvent {
                        change: c.id.clone(),
                        impact: m.id.clone(),
                        op: IMPACT_UPDATED,
                        post: co.post.clone(),
                        patch: json!({"props": {"title": "ghost"}})
                            .as_object()
                            .unwrap()
                            .clone(),
                        ..Default::default()
                    }],
                )
                .await,
            );
            let ds = must(g.drafts(&ctx, &mut tx, &c.id).await);
            assert!(
                ds.len() == 1 && ds[0].properties["title"] == "ghost",
                "the transaction does not read its own event: {ds:?}"
            );
            must(tx.rollback().await);
        }
        for (name, gg) in [("writer", g), ("other", &other)] {
            let (got, want) = (read_drafts(gg, &c.id).await, scratch_drafts(g, &c.id).await);
            assert_eq!(got, want, "{name}: a rolled-back write poisoned the cache");
        }
        // the cache never moved past the committed log
        let last = {
            let mut tx = must(g.begin(&ctx).await);
            must(impact_events(&ctx, &mut tx, &c.id).await)
                .last()
                .unwrap()
                .seq
        };
        let s = g.draft_states.get(&c.id);
        assert!(
            s.as_ref().is_some_and(|s| s.seq == last),
            "cache at {:?}, committed log at {last}",
            s.map(|s| s.seq)
        );

        // a committed write of the first graph reaches the second, which kept its cache
        edit_title(g, &c.id, &m.id, "seen").await;
        let mut tx = must(other.begin(&ctx).await);
        let ds = must(other.drafts(&ctx, &mut tx, &c.id).await);
        assert_eq!(ds.len(), 1, "other graph: {ds:?}");
        assert_eq!(
            ds[0].properties["title"], "seen",
            "the other graph does not see the edit"
        );
    })
    .await;
}

// A draft read from the cache is a copy: editing it does not change what the next read returns.
#[tokio::test]
async fn draft_cache_returns_copies() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let (c, m) = proposed(&f, "copy").await;
        edit_title(g, &c.id, &m.id, "x").await;
        {
            let mut tx = must(g.begin(&ctx).await);
            let mut ds = must(g.drafts(&ctx, &mut tx, &c.id).await);
            ds[0].properties.insert("title".into(), "mutated".into());
        }
        let (got, want) = (read_drafts(g, &c.id).await, scratch_drafts(g, &c.id).await);
        assert_eq!(got, want, "a caller edited the cache");
    })
    .await;
}
