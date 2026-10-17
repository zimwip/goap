//! activate_test.go, in process: the client over the handler, with no HTTP server between them.

use std::sync::Arc;

use goap_def::Domain;

use super::suspend::{alm_domain, ref_meth};
use super::versions::versioned;
use super::{as_, must_yaml};
use crate::client::Client;
use crate::handler::Handler;

// The client of the registry builds the activable form the registry builds (ADR 0126 step 6): the same compilation
// over the definition and the domains the version compiles against, here the version of alm it pinned at its
// publication, not the one in force since.
#[tokio::test]
async fn client_activates_as_the_registry() {
    let (s, d) = versioned();
    let ctx = as_(&["admin"]);
    let alm = |version: &str, extends: &str| -> Domain {
        let mut d = alm_domain(version);
        d.node_types[1].extends = extends.into();
        d
    };
    d.import_domain(&ctx, &must_yaml(&alm("1", "Requirement")), true)
        .await
        .expect("alm 1");
    s.import_published(&ctx, ref_meth("uses"))
        .await
        .expect("import");
    d.import_domain(&ctx, &must_yaml(&alm("2", "")), true)
        .await
        .expect("alm 2");

    let here = s.methodology(&ctx, "uses@1").await.expect("registry");
    let there = Client::with_service(Arc::new(Handler::new(s.clone())))
        .methodology(&ctx, "uses@1")
        .await
        .expect("client");
    assert!(
        !here.supertypes().is_empty() && here.supertypes() == there.supertypes(),
        "the types of the pinned domains: registry {:?}, client {:?}",
        here.supertypes(),
        there.supertypes()
    );
    assert!(
        here.definition().domains.get("alm").map(String::as_str) == Some("1")
            && there.definition().domains.get("alm").map(String::as_str) == Some("1"),
        "pins {:?} {:?}",
        here.definition().domains,
        there.definition().domains
    );
}
