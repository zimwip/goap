//! The guardian of the changes of this graph (ADR 0168): the `ChangeGuardianService` of the guardian service at
//! `GOAP_GUARDIAN_URL` (Go's `lib/guardianclient`). The graph knows no methodology; the guardian compiles those the
//! registry stores and answers on the blackboard sent.
use std::collections::BTreeMap;

use goap_connect::{Code, ConnectError, Ctx, Request};
use goap_proto::graph::v1 as pb;
use goap_proto::registry::v1::{self as registrypb, ChangeGuardianService as _};
use serde_json::Value;

use graph_core::{
    GraphError, Kind, Result,
    change::{Change, GuardianRef},
    guardian::{Board, GateResult, Guardian, StepDeps, activity_of},
};

use crate::{convert, graph::board_pb};

/// A client of the guardian service.
pub struct Client {
    rpc: registrypb::ChangeGuardianServiceClient,
}

impl Client {
    pub fn new(url: &str) -> Client {
        Client {
            rpc: registrypb::ChangeGuardianServiceClient::new(crate::platform::client(url)),
        }
    }
}

/// The graph error of a refusal of the guardian (`rpcerr.FromConnect`): its kind from its code, its message as is.
fn graph_error(e: ConnectError) -> GraphError {
    let kind = match e.code() {
        Code::InvalidArgument => Kind::Invalid,
        Code::NotFound => Kind::NotFound,
        Code::FailedPrecondition | Code::Aborted | Code::AlreadyExists => Kind::Conflict,
        Code::PermissionDenied | Code::Unauthenticated => Kind::Forbidden,
        _ => Kind::Internal,
    };
    GraphError::new(kind, e.message())
}

fn ctx() -> Ctx {
    Ctx::background()
}

/// The blackboard as the guardian reads it: what `GetBlackboard` answers, with the facets the graph adds.
fn blackboard(b: &Board) -> Result<registrypb::Blackboard> {
    let bb = board_pb(b)?;
    let mut facets = std::collections::HashMap::new();
    for (k, v) in &b.facets {
        facets.insert(k.clone(), serde_json::to_vec(v).map_err(GraphError::internal)?);
    }
    Ok(registrypb::Blackboard {
        change: bb.change,
        nodes: bb.nodes,
        neighbors: bb.neighbors,
        facets,
        at: bb.at,
        options: bb.options,
        active_option: bb.active_option,
        decision_points: bb.decision_points,
        ..Default::default()
    })
}

fn change(c: &Change) -> pb::Change {
    convert::change(c)
}

#[async_trait::async_trait]
impl Guardian for Client {
    async fn methodology_version(&self, methodology: &str) -> Result<String> {
        let r = self
            .rpc
            .resolve_methodology_version(
                &ctx(),
                Request::new(registrypb::ResolveMethodologyVersionRequest {
                    methodology: methodology.into(),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(r.into_inner().version)
    }

    async fn lifecycle(&self, methodology: &str) -> Result<Option<Value>> {
        let r = self
            .rpc
            .get_change_lifecycle(
                &ctx(),
                Request::new(registrypb::GetChangeLifecycleRequest {
                    methodology: methodology.into(),
                }),
            )
            .await
            .map_err(graph_error)?
            .into_inner();
        if r.lifecycle.is_empty() {
            return Ok(None);
        }
        serde_json::from_slice(&r.lifecycle)
            .map(Some)
            .map_err(GraphError::internal)
    }

    async fn default_goal(&self, methodology: &str) -> Result<String> {
        let r = self
            .rpc
            .get_default_goal(
                &ctx(),
                Request::new(registrypb::GetDefaultGoalRequest {
                    methodology: methodology.into(),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(r.into_inner().goal)
    }

    async fn guard(&self, board: &Board, expr: &str, transition: &str, decision: &str) -> Result<bool> {
        let r = self
            .rpc
            .check_guard(
                &ctx(),
                Request::new(registrypb::CheckGuardRequest {
                    blackboard: Some(blackboard(board)?),
                    expr: expr.into(),
                    transition: transition.into(),
                    decision: decision.into(),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(r.into_inner().ok)
    }

    async fn gate(&self, board: &Board, transition: &Value, decision: &str) -> Result<GateResult> {
        let r = self
            .rpc
            .check_gate(
                &ctx(),
                Request::new(registrypb::CheckGateRequest {
                    blackboard: Some(blackboard(board)?),
                    transition: serde_json::to_vec(transition).map_err(GraphError::internal)?,
                    decision: decision.into(),
                }),
            )
            .await
            .map_err(graph_error)?
            .into_inner();
        if r.result.is_empty() {
            return Ok(GateResult::default());
        }
        serde_json::from_slice(&r.result).map_err(GraphError::internal)
    }

    async fn commit(&self, c: &Change, board: &Board) -> Result<(bool, bool)> {
        if activity_of(c).is_empty() {
            return Ok((false, false)); // scoped to no Activity: the committable states decide
        }
        let r = self
            .rpc
            .check_commit(
                &ctx(),
                Request::new(registrypb::CheckCommitRequest {
                    change: Some(change(c)),
                    blackboard: Some(blackboard(board)?),
                }),
            )
            .await
            .map_err(graph_error)?
            .into_inner();
        Ok((r.decided, r.ok))
    }

    async fn guardians(&self, methodology: &str, project: &str) -> Result<Vec<GuardianRef>> {
        let r = self
            .rpc
            .get_guardians(
                &ctx(),
                Request::new(registrypb::GetGuardiansRequest {
                    methodology: methodology.into(),
                    project: project.into(),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(r.into_inner()
            .guardians
            .into_iter()
            .map(|g| GuardianRef {
                methodology: g.methodology,
                version: g.version,
            })
            .collect())
    }

    async fn integrates(&self, integrator: &str, methodology: &str) -> Result<bool> {
        let r = self
            .rpc
            .check_integrates(
                &ctx(),
                Request::new(registrypb::CheckIntegratesRequest {
                    integrator: integrator.into(),
                    methodology: methodology.into(),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(r.into_inner().integrates)
    }

    async fn check_guardians(&self, board: &Board, transition: &str, ancestors: &[Change]) -> Result<String> {
        let r = self
            .rpc
            .check_guardians(
                &ctx(),
                Request::new(registrypb::CheckGuardiansRequest {
                    blackboard: Some(blackboard(board)?),
                    transition: transition.into(),
                    ancestors: ancestors.iter().map(change).collect(),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(r.into_inner().refusal)
    }

    async fn check_sub_change(&self, parent: &Change, child: &Change) -> Result<()> {
        let (r, ancestor) = (activity_of(child), activity_of(parent));
        if r.is_empty() || ancestor.is_empty() || r == ancestor {
            return Ok(());
        }
        self.rpc
            .check_sub_change(
                &ctx(),
                Request::new(registrypb::CheckSubChangeRequest {
                    parent: Some(change(parent)),
                    child: Some(change(child)),
                }),
            )
            .await
            .map_err(graph_error)?;
        Ok(())
    }

    async fn operation_roles(&self, methodology: &str, operation: &str) -> Result<Option<Vec<String>>> {
        let r = self
            .rpc
            .get_operation_roles(
                &ctx(),
                Request::new(registrypb::GetOperationRolesRequest {
                    methodology: methodology.into(),
                    operation: operation.into(),
                }),
            )
            .await
            .map_err(graph_error)?
            .into_inner();
        Ok(r.restricted.then_some(r.roles))
    }

    async fn step_dependencies(&self, methodology: &str) -> Result<StepDeps> {
        // a guardian that cannot say leaves the history with no step relying on another (`StepDependencies`)
        let Ok(r) = self
            .rpc
            .get_step_dependencies(
                &ctx(),
                Request::new(registrypb::GetStepDependenciesRequest {
                    methodology: methodology.into(),
                }),
            )
            .await
        else {
            return Ok(StepDeps::new());
        };
        Ok(r.into_inner()
            .steps
            .into_iter()
            .map(|(step, on)| (step, on.names.into_iter().collect()))
            .collect::<BTreeMap<_, _>>())
    }
}
