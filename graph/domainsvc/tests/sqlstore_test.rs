mod common;

use chrono::{TimeDelta, TimeZone, Utc};
use common::{alm_domain, domain_stores};
use goap_ctx::Ctx;
use goap_def::{Domain, Record, STATUS_DRAFT, STATUS_PUBLISHED};

fn canon(d: &Domain) -> String {
    serde_json::to_value(d).unwrap().to_string() // map keys sorted
}

/// Every domain of the repository, the built-in ones included, round-trips through the stores.
#[tokio::test]
async fn domain_stores_round_trip_every_domain() {
    let mut ds: Vec<Domain> = Vec::new();
    let mut files: Vec<_> = std::fs::read_dir("../../domains")
        .unwrap()
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "yaml"))
        .collect();
    files.sort();
    for f in files {
        let src = std::fs::read(&f).unwrap();
        ds.push(goap_def::parse_domain(&src).unwrap_or_else(|e| panic!("{}: {e}", f.display())));
    }
    for (name, src) in goap_domains_builtin::FILES {
        ds.push(goap_def::parse_domain(src.as_bytes()).unwrap_or_else(|e| panic!("{name}: {e}")));
    }
    assert!(ds.len() >= 4, "domains: {}", ds.len());
    let stores = domain_stores("roundtrip").await;
    for (name, st) in &stores.all {
        let ctx = Ctx::background();
        let now = Utc.with_ymd_and_hms(2026, 9, 26, 10, 0, 0).unwrap();
        for d in &ds {
            st.save_domain(
                &ctx,
                Record {
                    domain: d.clone(),
                    status: STATUS_DRAFT,
                    created_at: Some(now),
                    updated_at: Some(now),
                    updated_by: "mia".into(),
                    ..Record::default()
                },
            )
            .await
            .unwrap();
            let got = st.get_domain(&ctx, &d.name, &d.version).await.unwrap();
            assert!(
                canon(&got.domain) == canon(d)
                    && got.status == STATUS_DRAFT
                    && got.created_at == Some(now)
                    && got.updated_by == "mia",
                "{name}: domain {} does not round-trip:\n have {}\n want {}",
                d.name,
                canon(&got.domain),
                canon(d)
            );
        }
    }
}

/// The stores keep the lifecycle of a version: a draft is replaced, a published version is immutable and in force,
/// only a draft is deleted.
#[tokio::test]
async fn domain_stores_lifecycle() {
    let stores = domain_stores("lifecycle").await;
    for (name, st) in &stores.all {
        let ctx = Ctx::background();
        let t0 = Utc.with_ymd_and_hms(2026, 9, 26, 10, 0, 0).unwrap();
        let draft = |d: Domain, created: Option<_>, updated| Record {
            domain: d,
            status: STATUS_DRAFT,
            created_at: created,
            updated_at: Some(updated),
            ..Record::default()
        };
        let mut d = alm_domain("1");
        st.save_domain(&ctx, draft(d.clone(), Some(t0), t0))
            .await
            .unwrap();
        d.description = "second draft".into();
        st.save_domain(&ctx, draft(d, None, t0 + TimeDelta::minutes(1)))
            .await
            .unwrap();
        let got = st.get_domain(&ctx, "alm", "1").await.unwrap();
        assert!(
            got.domain.description == "second draft" && got.created_at == Some(t0),
            "{name}: a draft is replaced and keeps its creation: {got:?}"
        );
        let err = st.get_domain(&ctx, "alm", "").await.unwrap_err();
        assert!(err.is_not_found(), "{name}: no published version: {err}");
        for (i, v) in ["1", "2"].iter().enumerate() {
            if *v != "1" {
                st.save_domain(&ctx, draft(alm_domain(v), Some(t0), t0))
                    .await
                    .unwrap();
            }
            st.set_domain_status(
                &ctx,
                "alm",
                v,
                STATUS_PUBLISHED,
                t0 + TimeDelta::hours(i as i64 + 1),
            )
            .await
            .unwrap();
        }
        let got = st.get_domain(&ctx, "alm", "").await.unwrap();
        assert!(
            got.domain.version == "2" && got.published_at == Some(t0 + TimeDelta::hours(2)),
            "{name}: the latest published version is in force: {got:?}"
        );
        let err = st
            .save_domain(&ctx, draft(alm_domain("2"), None, t0))
            .await
            .unwrap_err();
        assert!(
            err.is_immutable(),
            "{name}: a published version is immutable: {err}"
        );
        let err = st.delete_domain(&ctx, "alm", "2").await.unwrap_err();
        assert!(err.is_immutable(), "{name}: only a draft is deleted: {err}");
        st.save_domain(&ctx, draft(alm_domain("3"), Some(t0), t0))
            .await
            .unwrap();
        st.delete_domain(&ctx, "alm", "3").await.unwrap();
        let list = st.list_domains(&ctx).await.unwrap();
        assert_eq!(list.len(), 2, "{name}: list: {list:?}");
        let err = st
            .set_domain_status(&ctx, "alm", "9", STATUS_PUBLISHED, t0)
            .await
            .unwrap_err();
        assert!(err.is_not_found(), "{name}: unknown version: {err}");
    }
}
