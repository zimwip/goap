//! Port of `client.go`: the registry as the engine sees it (engine.MethodologyPort) over its Connect service.

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_connect::{Code, Ctx, Request};
use goap_domain::split_methodology_ref;
use goap_domainpb as domainpb;
use goap_gen::registry::v1::{self as pb, RegistryService, RegistryServiceClient};
use goap_methodology::Compiled;

use crate::compilecache::{activate, types_stamp};
use crate::convert::from_pb;
use crate::error::{Error, Result, SuspendedError};
use crate::store::{STATUS_PUBLISHED, key};

/// Client adapts the registry to engine.MethodologyPort. It builds the activable form of a version as the registry
/// does (activate over the definition and the domains the registry says it compiles against); published versions are
/// immutable, so compiled methodologies are cached by name, version and those domains.
pub struct Client {
    pub(crate) rpc: Arc<dyn RegistryService>,
    cache: parking_lot::Mutex<HashMap<String, Arc<Compiled>>>,
}

impl Client {
    /// NewClient returns a registry client over a Connect client of the service.
    pub fn new(client: goap_connect::Client) -> Client {
        Client::with_service(Arc::new(RegistryServiceClient::new(client)))
    }

    /// A client of the service at base_url, default settings.
    pub fn connect(base_url: &str) -> Client {
        Client::with_service(Arc::new(RegistryServiceClient::connect(base_url)))
    }

    /// A client over any implementation of the service (its handler in process, a test double).
    pub fn with_service(rpc: Arc<dyn RegistryService>) -> Client {
        Client {
            rpc,
            cache: parking_lot::Mutex::new(HashMap::new()),
        }
    }

    /// List: latest published versions, the suspended ones left out (ADR 0125 §3).
    pub async fn list(&self, ctx: &Ctx) -> Result<Vec<Arc<Compiled>>> {
        let r = self
            .rpc
            .list_methodologies(ctx, Request::new(pb::ListMethodologiesRequest::default()))
            .await?;
        let mut out = Vec::new();
        for m in &r.msg.methodologies {
            if m.status != STATUS_PUBLISHED.as_str() || !m.suspended.is_empty() {
                continue; // a suspended version is not in force (ADR 0125 §3)
            }
            out.push(self.methodology(ctx, &m.name).await?);
        }
        Ok(out)
    }

    /// Methodology: the version a reference names (domain.MethodologyRef, ADR 0113), else the latest published one.
    pub async fn methodology(&self, ctx: &Ctx, r#ref: &str) -> Result<Arc<Compiled>> {
        let (name, version) = split_methodology_ref(r#ref);
        let r = match self
            .rpc
            .get_methodology(
                ctx,
                Request::new(pb::GetMethodologyRequest {
                    name: name.to_string(),
                    version: version.to_string(),
                    with_domains: true,
                }),
            )
            .await
        {
            Err(e) if e.code() == Code::NotFound => {
                return Err(Error::UnknownMethodology(r#ref.to_string()));
            }
            Err(e) => return Err(e.into()),
            Ok(r) => r.msg,
        };
        let pm = r.methodology.unwrap_or_default();
        let ds: Vec<_> = r
            .domains
            .iter()
            .map(|p| domainpb::domain_from_pb(Some(p)))
            .collect();
        // the version and the domains it compiles against: a version that pins none follows the domains in force
        let vk = key(&pm.name, &pm.version);
        let k = format!("{vk}|{}", types_stamp(&ds));
        if !pm.suspended.is_empty() {
            return Err(Error::Suspended(SuspendedError {
                r#ref: vk,
                cause: pm.suspended.clone(),
                graph_invalid: false,
            }));
        }
        if let Some(m) = self.cache.lock().get(&k) {
            return Ok(m.clone());
        }
        // the activable form the registry builds, by the same compilation of the same inputs (ADR 0126 step 6)
        let cm = Arc::new(activate(&from_pb(Some(&pm)), &ds)?);
        Ok(self.cache.lock().entry(k).or_insert(cm).clone())
    }
}

#[async_trait]
impl goap_engine::MethodologyPort for Client {
    async fn methodology(&self, ctx: &Ctx, name: &str) -> goap_engine::Result<Arc<Compiled>> {
        Client::methodology(self, ctx, name)
            .await
            .map_err(Error::into_engine)
    }

    async fn list(&self, ctx: &Ctx) -> goap_engine::Result<Vec<Arc<Compiled>>> {
        Client::list(self, ctx).await.map_err(Error::into_engine)
    }
}
