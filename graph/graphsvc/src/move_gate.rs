//! Port of `move.go`: the gate of a move of changes to another project (ADR 0091).

use std::sync::Arc;

use goap_access::Directory;
use goap_authz::{Authorizer, Request, Resource, check, principal_from};
use goap_ctx::Ctx;
use goap_domain::Change;
use goap_graph::{Error, ErrorKind, Result};

use crate::lifecycle::authz_error;

/// ProjectMoveGate is the gate of a move of changes to another project (ADR 0091), plugged by the compositions that
/// hold the organisation (cmd/graph, goap-dev): the caller needs change:move on the project of each change and on the
/// target, and access to both (access::Snapshot::may_access_project); and the methodology of each change must apply
/// (its own or inherited, Snapshot::applicable_methodologies) to both. A change with no methodology is not held to
/// the second rule. A platform service (Principal::is_system) is not asked the first two. Either argument may be
/// unset: the check it does is then skipped.
pub fn project_move_gate(
    a: Option<Arc<dyn Authorizer>>,
    d: Option<Arc<Directory>>,
) -> Arc<dyn goap_graph::ProjectMoveGate> {
    Arc::new(MoveGate { a, d })
}

struct MoveGate {
    a: Option<Arc<dyn Authorizer>>,
    d: Option<Arc<Directory>>,
}

#[async_trait::async_trait]
impl goap_graph::ProjectMoveGate for MoveGate {
    async fn check(&self, ctx: &Ctx, family: &[Change], to: &str) -> Result<()> {
        let who = principal_from(ctx);
        let snap = match &self.d {
            Some(d) => Some(
                d.snapshot(ctx)
                    .await
                    .map_err(|e| Error::other(e).context("the organisation cannot be read"))?,
            ),
            None => None,
        };
        for c in family {
            if !who.is_system() {
                for project in [c.project_id.as_str(), to] {
                    if let Some(s) = &snap
                        && !s.may_access_project(&who, project)
                    {
                        return Err(authz_error(goap_authz::Error::Forbidden(String::new()))
                            .context(format!("you may not work on project {project}")));
                    }
                    if let Some(a) = &self.a {
                        check(
                            ctx,
                            Some(a.as_ref()),
                            &Request {
                                subject: who.clone(),
                                action: "move".into(),
                                resource: Resource {
                                    r#type: "change".into(),
                                    id: c.id.to_string(),
                                    name: c.title.clone(),
                                    org: c.owner_org.clone(),
                                    project_id: project.to_string(),
                                    ..Default::default()
                                },
                            },
                        )
                        .await
                        .map_err(|e| {
                            authz_error(e)
                                .context(format!("moving change {} on project {project}", c.id))
                        })?;
                    }
                }
            }
            let Some(s) = &snap else { continue };
            if c.methodology.is_empty() {
                continue;
            }
            for project in [c.project_id.as_str(), to] {
                if !s.applicable_methodologies(project).contains(&c.methodology) {
                    return Err(Error::wrap(
                        ErrorKind::Invalid,
                        format!(
                            "methodology {} of change {} does not apply to project {project} (a change moves between projects that both apply its methodology)",
                            c.methodology, c.id
                        ),
                    ));
                }
            }
        }
        Ok(())
    }
}
