//! Command events serves the one event stream of the web (ADR 0053): it follows every publication of the platform
//! on the bus and streams them, with the presence of the users, to the browsers.

use std::sync::Arc;

use goap_access::{Authorizer, Directory};
use goap_ctx::Ctx;
use goap_eventsvc::{Handler, Hub, PRESENCE_SUBJECT};
use goap_gen::events::v1::event_service_router;
use goap_platform::{Server, env, fatal, h2c_client, logger, optional_events};

/// Why the service refuses to start without a bus.
const ERR_NO_BUS: &str = "GOAP_NATS_URL is required: the event stream follows the bus";

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("events");
    let tel = goap_telemetry::setup(&ctx, "events");
    let Some(bus) = optional_events(&ctx).await else {
        fatal("events", ERR_NO_BUS);
    };
    let graph = Arc::new(goap_graphclient::Client::from_connect(
        goap_graphclient::Client::builder(&env("GOAP_GRAPH_URL", "http://localhost:8081"))
            .http(h2c_client())
            .interceptors(&goap_telemetry::client_options())
            .build(),
    ));
    let authorizer = match Authorizer::new(Arc::new(Directory::new(graph))) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    let mut hub = Hub::new();
    hub.authz = Some(authorizer);
    // presence is shared between replicas over core NATS, outside the persisted GOAP stream
    {
        let bus = bus.clone();
        hub.relay = Some(Arc::new(move |subject: &str, v: &serde_json::Value| {
            let (bus, subject, v) = (bus.clone(), subject.to_string(), v.clone());
            tokio::spawn(async move {
                let _ = bus.publish_core(&subject, &v).await;
            });
        }));
    }
    let hub = Arc::new(hub);
    let (run_ctx, stop) = ctx.with_cancel();
    {
        let hub = hub.clone();
        tokio::spawn(async move { hub.run(&run_ctx).await });
    }
    for subject in ["goap.>".to_string(), format!("{PRESENCE_SUBJECT}>")] {
        let hub = hub.clone();
        if let Err(e) = bus
            .subscribe_subject(&subject, move |subject, data, st| {
                hub.ingest(&subject, &data, st)
            })
            .await
        {
            fatal("subscribe", e);
        }
    }
    hub.hello();
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    srv.mount(event_service_router(
        Arc::new(Handler::new(hub.clone())),
        goap_telemetry::handler_options(),
    ));
    let res = srv.run().await;
    stop.cancel();
    bus.close().await;
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}
