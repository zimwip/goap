use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use chrono::{TimeZone, Utc};
use goap_connect::{ConnectError, Ctx, Request, Response};
use goap_gen::registry::v1 as pb;

use crate::{Client, Error};

/// A registry of two versions of `alm` (1 published before 2) and a draft 3.
#[derive(Default)]
struct Fake {
    lists: AtomicUsize,
}

fn stored() -> Vec<pb::Domain> {
    let at = |s| Some(Utc.timestamp_opt(s, 0).unwrap().into());
    vec![
        pb::Domain {
            name: "alm".into(),
            version: "1".into(),
            status: "published".into(),
            published_at: at(100),
            ..Default::default()
        },
        pb::Domain {
            name: "alm".into(),
            version: "2".into(),
            status: "published".into(),
            published_at: at(200),
            ..Default::default()
        },
        pb::Domain {
            name: "alm".into(),
            version: "3".into(),
            status: "draft".into(),
            ..Default::default()
        },
    ]
}

#[goap_connect::async_trait]
impl pb::DomainService for Fake {
    async fn list_domains(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListDomainsRequest>,
    ) -> Result<Response<pb::ListDomainsResponse>, ConnectError> {
        assert!(req.msg.all_versions);
        self.lists.fetch_add(1, Ordering::SeqCst);
        Ok(Response::new(pb::ListDomainsResponse {
            domains: stored()
                .into_iter()
                .map(|d| pb::DomainSummary {
                    name: d.name,
                    version: d.version,
                    ..Default::default()
                })
                .collect(),
        }))
    }

    async fn get_domain(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetDomainRequest>,
    ) -> Result<Response<pb::GetDomainResponse>, ConnectError> {
        let d = stored()
            .into_iter()
            .find(|d| d.name == req.msg.name && d.version == req.msg.version);
        match d {
            Some(d) => Ok(Response::new(pb::GetDomainResponse { domain: Some(d) })),
            None => Err(ConnectError::not_found("no such domain")),
        }
    }
}

#[tokio::test]
async fn reads_once_until_invalidated() {
    let fake = Arc::new(Fake::default());
    let c = Client::with_service(fake.clone());
    let ctx = Ctx::background();
    assert_eq!(c.domain_versions(&ctx, true).await.unwrap().len(), 3);
    assert_eq!(c.domain_versions(&ctx, false).await.unwrap().len(), 1);
    assert_eq!(fake.lists.load(Ordering::SeqCst), 1);
    c.invalidate();
    c.domains(&ctx).await.unwrap();
    assert_eq!(fake.lists.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn reads_again_after_the_ttl() {
    let fake = Arc::new(Fake::default());
    let c = Client::with_service(fake.clone()).with_ttl(Duration::from_millis(1));
    let ctx = Ctx::background();
    c.domains(&ctx).await.unwrap();
    tokio::time::sleep(Duration::from_millis(5)).await;
    c.domains(&ctx).await.unwrap();
    assert_eq!(fake.lists.load(Ordering::SeqCst), 2);
}

#[tokio::test]
async fn get_domain_latest_published_or_exact() {
    let c = Client::with_service(Arc::new(Fake::default()));
    let ctx = Ctx::background();
    assert_eq!(
        c.get_domain(&ctx, "alm", "").await.unwrap().domain.version,
        "2"
    );
    assert_eq!(
        c.get_domain(&ctx, "alm", "3").await.unwrap().domain.version,
        "3"
    );
    let err = c.get_domain(&ctx, "alm", "9").await.unwrap_err();
    assert!(err.is_not_found());
    assert_eq!(err.to_string(), "alm@9: domain not found");
    assert!(matches!(
        c.get_domain(&ctx, "nope", "").await,
        Err(Error::NotFound(_))
    ));
}
