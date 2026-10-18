//! Command registry is the methodology registry: it stores the methodologies (in the graph) and compiles them against
//! the domains of the domain registry, served by the graph (ADR 0125 §3).

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_access::{Authorizer, Directory, ROLE_ADMIN};
use goap_ctx::Ctx;
use goap_engine::{Payload, Publisher};
use goap_gen::registry::v1::registry_service_router;
use goap_platform::{Events, Server, env, fatal, h2c_client, logger, optional_events};
use goap_registrysvc::{
    ApplicableFn, BoxError, GraphStore, Handler, Service, applicable_fn, mcp_scopes_fn,
};

/// The subjects of the domain registry after which the methodologies are checked again (ADR 0125 §3).
const DOMAIN_SUBJECTS: [&str; 2] = [
    "goap.registry.domain.published",
    "goap.registry.domain.deleted",
];

/// The longest wait between two attempts of a bootstrap step.
const MAX_DELAY: Duration = Duration::from_secs(60);

/// Bus publishes the events of the registry on NATS (Go's `*platform.Events` as the service's publisher).
struct Bus(Events);

#[async_trait]
impl Publisher for Bus {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Payload<'_>) -> goap_engine::Result<()> {
        self.0
            .publish(ctx, subject, &v.json())
            .await
            .map_err(|e| goap_engine::Error::Msg(e.to_string()))
    }
}

/// The next delay of a retry: doubled, at most a minute.
fn next_delay(d: Duration) -> Duration {
    (2 * d).min(MAX_DELAY)
}

/// applicable lists the methodologies applicable to a project from the snapshot of the organisation (ADR 0106:
/// where the guardian chain of a change finds the methodologies integrating its own).
fn applicable(d: Arc<Directory>) -> ApplicableFn {
    applicable_fn(move |ctx: Ctx, project: String| {
        let d = d.clone();
        async move {
            let s = d
                .snapshot(&ctx)
                .await
                .map_err(|e| Box::new(e) as BoxError)?;
            Ok(s.applicable_methodologies(&project))
        }
    })
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("registry");
    let tel = goap_telemetry::setup(&ctx, "registry");
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    let events = optional_events(&ctx).await;
    let publisher: Option<Arc<dyn Publisher>> = events
        .clone()
        .map(|e| Arc::new(Bus(e)) as Arc<dyn Publisher>);

    // methodologies are nodes of the graph; the domains are the graph's (ADR 0125 §3), read from its domain registry
    let graph_url = env("GOAP_GRAPH_URL", "http://localhost:8081");
    let graph_client = Arc::new(goap_graphclient::Client::from_connect(
        goap_graphclient::Client::builder(&graph_url)
            .http(h2c_client())
            .interceptors(&goap_telemetry::client_options())
            .build(),
    ));
    let store = Arc::new(GraphStore::new(graph_client.clone()));
    let domains = Arc::new(goap_domainclient::Client::new(
        goap_connect::Client::builder(&graph_url)
            .http(h2c_client())
            .interceptors(&goap_telemetry::client_options())
            .build(),
    ));
    let directory = Arc::new(Directory::new(graph_client.clone()));
    let authorizer = match Authorizer::new(directory.clone()) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    let mut svc = Service::new(store.clone());
    svc.domains = Some(domains.clone());
    svc.authz = Some(authorizer);
    svc.events = publisher.clone();
    // the scope of the MCPs (ADR 0028)
    let mcps = Arc::new(goap_mcpsvc::Directory::new(graph_client.clone()));
    svc.mcp_scopes = Some(mcp_scopes_fn(move |ctx: Ctx| {
        let mcps = mcps.clone();
        async move { mcps.scopes(&ctx).await.map_err(|e| Box::new(e) as BoxError) }
    }));
    // the integrators in force in a project (ADR 0106)
    svc.applicable = Some(applicable(directory));
    let svc = Arc::new(svc);

    let dir = env("GOAP_METHODOLOGIES_DIR", "");
    if !dir.is_empty() {
        // bootstrap: import the YAML files of versions not stored yet, once the graph answers
        let system =
            goap_authz::with_principal(&ctx, goap_authz::system("registry", &[ROLE_ADMIN]));
        let mut seed = Service::new(store.clone());
        seed.domains = Some(domains.clone());
        seed.events = publisher.clone();
        tokio::spawn(async move {
            let mut delay = Duration::from_secs(1);
            loop {
                match seed.seed(&system, &dir).await {
                    Ok(loaded) => {
                        tracing::info!(dir = %dir, methodologies = ?loaded, "methodologies imported");
                        return;
                    }
                    Err(e) => {
                        tracing::warn!(err = %format!("import methodologies: {}", e.error), retry = ?delay, "registry bootstrap");
                    }
                }
                tokio::time::sleep(delay).await;
                delay = next_delay(delay);
            }
        });
    }

    // a domain published or archived: the methodologies are checked again, the ones it breaks are suspended (ADR
    // 0125 §3); one check at a time, in the order of the events
    if let Some(bus) = &events {
        let serial = Arc::new(tokio::sync::Mutex::new(()));
        for subject in DOMAIN_SUBJECTS {
            let (svc, domains, serial) = (svc.clone(), domains.clone(), serial.clone());
            let res = bus
                .subscribe(subject, move |_data| {
                    domains.invalidate();
                    let (svc, serial) = (svc.clone(), serial.clone());
                    tokio::spawn(async move {
                        let _held = serial.lock().await;
                        if let Err(e) = svc.domains_changed(&Ctx::background()).await {
                            tracing::error!(err = %e, "check the methodologies");
                        }
                    });
                })
                .await;
            if let Err(e) = res {
                fatal("subscribe", e);
            }
        }
    }
    {
        // the domains of a new start may break a methodology too (a built-in domain changes with the platform)
        let (svc, ctx) = (svc.clone(), ctx.clone());
        tokio::spawn(async move {
            let mut delay = Duration::from_secs(1);
            while svc.recheck(&ctx).await.is_err() {
                tokio::time::sleep(delay).await;
                delay = next_delay(delay);
            }
        });
    }

    srv.mount(registry_service_router(
        Arc::new(Handler::new(svc)),
        goap_telemetry::handler_options().with(goap_identity::command_interceptor()),
    ));
    let res = srv.run().await;
    if let Some(bus) = events {
        bus.close().await;
    }
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn delays_double_up_to_a_minute() {
        let mut d = Duration::from_secs(1);
        let mut seen = vec![];
        for _ in 0..8 {
            seen.push(d.as_secs());
            d = next_delay(d);
        }
        assert_eq!(seen, vec![1, 2, 4, 8, 16, 32, 60, 60]);
    }
}
