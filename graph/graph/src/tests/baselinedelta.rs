//! A baseline is stored as a delta from its parent, whole every checkpoint_every
//! baselines (ADR 0032); reading it back gives what was written. Old whole copies are compacted to deltas.

use goap_domain::Baseline;

use crate::baselinedelta::{CHECKPOINT_LOCK, checkpoint_every, set_checkpoint_every};
use crate::testsupport::*;
use crate::{default_materialize_every, set_default_materialize_every};

/// Restores the process-wide intervals when a test ends.
struct Intervals(i64, usize);

impl Drop for Intervals {
    fn drop(&mut self) {
        set_checkpoint_every(self.0);
        set_default_materialize_every(self.1);
    }
}

fn intervals(every: i64, materialize: usize) -> Intervals {
    let old = Intervals(checkpoint_every(), default_materialize_every());
    set_checkpoint_every(every);
    set_default_materialize_every(materialize);
    old
}

/// storedEntryCounts counts the stored entries of each baseline (None for the memory store).
async fn stored_entry_counts(r: &TestRepo, bs: &[Baseline]) -> Option<Vec<i64>> {
    let mut out = Vec::new();
    for b in bs {
        let n: i64 = match &r.store {
            Store::Memory => return None,
            Store::Sqlite(p) => {
                sqlx::query_scalar("SELECT count(*) FROM baseline_entry WHERE baseline_id = ?")
                    .bind(b.id.as_str())
                    .fetch_one(p)
                    .await
                    .expect("count")
            }
            Store::Postgres(p) => sqlx::query_scalar(
                "SELECT count(*) FROM baseline_entry WHERE baseline_id = $1::uuid",
            )
            .bind(b.id.as_str())
            .fetch_one(p)
            .await
            .expect("count"),
        };
        out.push(n);
    }
    Some(out)
}

#[tokio::test]
async fn baseline_deltas() {
    let _lock = CHECKPOINT_LOCK.lock().await;
    let _restore = intervals(3, 1); // every baseline materialised: what is stored are its entries
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let mut written = vec![f.base.clone()];
        for i in 0..7 {
            let mut ls = vec![landing(&format!("DES-{i}"), "Design")];
            match i {
                2 => ls.push(Landing {
                    properties: props(&[("step", i.into())]),
                    ..landing("REQ-1", "Requirement")
                }),
                4 => ls.push(Landing {
                    properties: props(&[("obsolete", true.into())]),
                    ..landing("TST-1", "TestCase")
                }),
                5 => ls.push(Landing {
                    deleted: true,
                    ..landing("DES-0", "Design")
                }),
                _ => {}
            }
            written.push(must(land(g, "", &ls).await));
        }
        for (i, want) in written.iter().enumerate() {
            let got = must(g.baseline(&c, &want.id).await);
            assert_eq!(got.nodes, want.nodes, "baseline {i}");
            let (nodes, _) = must(g.baseline_graph(&c, &want.id).await);
            assert_eq!(nodes.len(), want.nodes.len(), "baseline {i}");
            for n in &nodes {
                assert!(
                    want.contains(&n.r#ref()),
                    "baseline {i}: {} is not in it",
                    n.r#ref()
                );
            }
        }
        // what is stored: whole at a checkpoint, the difference with the parent otherwise
        let Some(counts) = stored_entry_counts(r, &written).await else {
            return; // the memory store keeps whole maps
        };
        let deltas = written
            .iter()
            .zip(&counts)
            .filter(|(b, n)| (**n as usize) < b.nodes.len())
            .count();
        assert!(deltas > 0, "no baseline stored as a delta: {counts:?}");
    })
    .await;
}

// Baselines stored whole are compacted to deltas without changing their content, and compacting again changes
// nothing.
#[tokio::test]
async fn compact_baselines() {
    let _lock = CHECKPOINT_LOCK.lock().await;
    let _restore = intervals(1, 1); // every baseline written whole, as before deltas
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        set_checkpoint_every(1);
        let f = new_fixture(r).await;
        let g = &f.g;
        let mut written = vec![f.base.clone()];
        for i in 0..6 {
            written.push(must(
                land(g, "", &[landing(&format!("DES-{i}"), "Design")]).await,
            ));
        }
        let before = stored_entry_counts(r, &written).await;
        set_checkpoint_every(4);
        let n = must(g.compact_baselines(&c).await);
        for (i, want) in written.iter().enumerate() {
            let got = must(g.baseline(&c, &want.id).await);
            assert_eq!(got.nodes, want.nodes, "baseline {i} after compaction");
        }
        let Some(before) = before else {
            assert_eq!(n, 0, "the memory store keeps whole maps");
            return;
        };
        let after = stored_entry_counts(r, &written).await.expect("counts");
        let total = |cs: &[i64]| cs.iter().sum::<i64>();
        assert!(
            n > 0 && total(&after) < total(&before),
            "compacted {n}: {before:?} -> {after:?}"
        );
        assert_eq!(must(g.compact_baselines(&c).await), 0, "second compaction");
        // a baseline written after the compaction continues the chains
        let next = must(land(g, "", &[landing("DES-X", "Design")]).await);
        assert_eq!(must(g.baseline(&c, &next.id).await).nodes, next.nodes);
    })
    .await;
}
