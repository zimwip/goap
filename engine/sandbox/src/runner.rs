use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_connect::{Code, ConnectError, Interceptors, Request, Response};
use goap_ctx::Ctx;
use goap_dsl as dsl;
use goap_gen::runtime::v1::{
    ExecuteRequest, ExecuteResponse, LogLine, RuntimeServiceClient, SandboxService,
};
use serde::Deserialize;

use crate::runtime::RemoteHost;

/// Runner is the SandboxService served by goap-runner inside a sandbox.
#[derive(Clone, Default)]
pub struct Runner {
    /// HTTP is used to call back the engine RuntimeService (default: a plain client).
    pub http: Option<reqwest::Client>,
    /// Options are applied to the RuntimeService client (tracing interceptors).
    pub options: Interceptors,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Snapshot {
    items: Option<Vec<dsl::Item>>,
    nodes: Option<Vec<dsl::ChangeImpact>>,
    intent: Option<String>,
    goal: Option<String>,
}

pub(crate) fn timestamp(t: &DateTime<Utc>) -> pbjson_types::Timestamp {
    pbjson_types::Timestamp {
        seconds: t.timestamp(),
        nanos: t.timestamp_subsec_nanos() as i32,
    }
}

#[async_trait]
impl SandboxService for Runner {
    async fn execute(
        &self,
        ctx: &Ctx,
        req: Request<ExecuteRequest>,
    ) -> Result<Response<ExecuteResponse>, ConnectError> {
        let m = req.into_inner();
        let mut job = dsl::Job {
            language: m.language,
            code: m.code,
            process_id: m.process_id,
            agent: m.agent,
            action: m.action,
            timeout: Duration::from_millis(u64::try_from(m.timeout_ms).unwrap_or(0)),
            ..Default::default()
        };
        if !m.blackboard_json.is_empty() {
            let s: Snapshot = serde_json::from_str(&m.blackboard_json)
                .map_err(|e| ConnectError::from_error(Code::InvalidArgument, e))?;
            job.items = s.items.unwrap_or_default();
            job.nodes = s.nodes.unwrap_or_default();
            job.intent = s.intent.unwrap_or_default();
            job.goal = s.goal.unwrap_or_default();
        }
        if let Ok(Some(p)) = serde_json::from_str(&m.params_json) {
            job.params = p;
        }
        if let Ok(Some(v)) = serde_json::from_str(&m.vars_json) {
            job.vars = v;
        }
        let client = goap_connect::Client::builder(&m.runtime_url)
            .http(self.http.clone().unwrap_or_default())
            .interceptors(&self.options)
            .build();
        let host = RemoteHost {
            client: RuntimeServiceClient::new(client),
            job_id: m.job_id,
            token: m.token,
        };
        let (res, err) = dsl::run(ctx, job, &host).await;
        let mut out = ExecuteResponse {
            output: res.output,
            suspended: res.suspended,
            ..Default::default()
        };
        for l in &res.logs {
            out.logs.push(LogLine {
                time: Some(timestamp(&l.time)),
                level: l.level.clone(),
                message: l.message.clone(),
            });
        }
        match err {
            Err(e) => out.error = e.to_string(),
            Ok(()) => {
                out.items_json = serde_json::to_string(&res.items).unwrap_or_default();
                out.nodes_json = serde_json::to_string(&res.nodes).unwrap_or_default();
            }
        }
        Ok(Response::new(out))
    }
}
