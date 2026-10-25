//! The demo data (Go's `platform/devseed` behind `LoadDemoData`): the demo organisation and project, then the ALM
//! repository of the demo, each seeded when it is missing, from the file the Go composition loads too.
use super::*;
use graph_catalog::catalog::STRUCTURE_ORGANISATION;
use graph_core::model::Node;
use serde::Deserialize;
use serde_json::{Map, Value as Json, json};

/// The demo itself (`platform/devseed/demo.json`), shared with the Go composition.
const DEMO_JSON: &str = include_str!("../../../../../platform/devseed/demo.json");

/// The namespaces and types the demo writes (`access.NamespaceOrganisation`, `NodeTypeOrgUnit`...).
const ORGANISATION: &str = "organisation";
const ALM: &str = "alm";
const ORG_UNIT: &str = "organisation@OrgUnit";
const PROJECT_UNIT: &str = "organisation@ProjectUnit";
const ASSIGNMENT: &str = "organisation@Assignment";
const PART_OF: &str = "organisation@part_of";
const PROJECT_PART_OF: &str = "organisation@project_part_of";
const ASSIGNS_ORG: &str = "organisation@assigns_org";
const ASSIGNS_PROJECT: &str = "organisation@assigns_project";
/// The unit the organisation of the demo hangs from, and holding the demo project's roles.
const ACME: &str = "ORG-ACME";

#[derive(Deserialize)]
struct Demo {
    project: Project,
    assignment: Assignment,
    units: Vec<Unit>,
    owners: std::collections::HashMap<String, String>,
    nodes: Vec<DemoNode>,
    links: Vec<[String; 3]>,
}

#[derive(Deserialize)]
struct Project {
    key: String,
    name: String,
    description: String,
    kind: String,
    status: String,
    methodologies: Vec<String>,
}

#[derive(Deserialize)]
struct Assignment {
    roles: Vec<String>,
    description: String,
}

#[derive(Deserialize)]
struct Unit {
    key: String,
    name: String,
    kind: String,
    parent: String,
}

#[derive(Deserialize)]
struct DemoNode {
    key: String,
    #[serde(rename = "type")]
    typ: String,
    properties: Map<String, Json>,
    #[serde(default)]
    state: String,
}

/// A node of a seed (`graphsvc.SeedNode`).
fn seed_node(key: &str, typ: &str, props: Map<String, Json>) -> NodeEdit {
    NodeEdit {
        key: key.into(),
        typ: typ.into(),
        props: Some(props),
        rationale: format!("Seed {key}"),
        ..NodeEdit::default()
    }
}

fn link_to(e: &mut NodeEdit, typ: &str, to: NodeRef) {
    e.links.push(LinkEdit {
        typ: typ.into(),
        to: Some(to),
        ..LinkEdit::default()
    });
}

fn link_key(e: &mut NodeEdit, typ: &str, key: &str) {
    e.links.push(LinkEdit {
        typ: typ.into(),
        to_key: key.into(),
        ..LinkEdit::default()
    });
}

fn object(v: Json) -> Map<String, Json> {
    match v {
        Json::Object(m) => m,
        _ => Map::new(),
    }
}

/// The latest version on main of the node of a key (`Graph.NodeByKey`), if any.
async fn by_key(tx: &Tx<'_, Session>, ns: &str, key: &str) -> Result<Option<Node>, GraphError> {
    let Some(id) = tx.store.node_id_by_key(ns, key).await? else {
        return Ok(None);
    };
    tx.store.latest_on(&id, graph_core::model::MAIN_BRANCH).await
}

async fn root(tx: &Tx<'_, Session>, kind: &str) -> Result<NodeRef, GraphError> {
    let key = tx.structure_root(kind)?;
    by_key(tx, ORGANISATION, &key)
        .await?
        .map(|n| n.node_ref())
        .ok_or_else(|| GraphError::not_found(format!("node key {key:?}")))
}

impl Graph {
    /// `LoadDemoData` (Go's `Handler.LoadDemoData` over `devseed.DemoLoader`): for a caller the floor lets write the
    /// policies, the demo organisation and project in one change of the organisation, then the ALM data in one
    /// change of the demo project, each when it is missing, written as the graph service for the caller.
    pub(super) async fn load_demo_data_rpc(
        &self,
        req: Request<pb::LoadDemoDataRequest>,
    ) -> Answer<pb::LoadDemoDataResponse> {
        let (caller, stamp) = self.identified(&req).await?;
        let d: Demo = serde_json::from_str(DEMO_JSON).map_err(|e| ConnectError::internal(format!("demo data: {e}")))?;
        {
            let tx = self.state.reader().await.map_err(connect_error)?;
            let snap = access::Snapshot::read(&tx).await.map_err(connect_error)?;
            let res = abac::Resource {
                typ: "policy".into(),
                org: caller.org.clone(),
                ..abac::Resource::default()
            };
            if !snap
                .floor(&caller.reader_or_self(), &res, "write")
                .map_err(connect_error)?
            {
                return stamp.on(Err(connect_error(forbidden(&caller.subject, "write", "policy", ""))));
            }
        }
        // the seeds act as the graph service for the person (`SystemPrincipal.For`)
        let system = Caller {
            subject: "system:graph".into(),
            on_behalf_of: end_user_of(&caller).into(),
            at_least: Vec::new(),
            project: String::new(),
            command: caller.command.clone(),
            roles: vec![ROLE_ADMIN.into()],
            org: String::new(),
        };
        let org = self
            .write_on(system.clone(), &[], async |tx, acc| {
                demo_organisation(tx, acc, &d).await
            })
            .await?
            .into_inner();
        let repo = self
            .write_on(system, &[], async |tx, acc| demo_repository(tx, acc, &d).await)
            .await?
            .into_inner();
        stamp.on(Ok(Response::new(pb::LoadDemoDataResponse {
            loaded: org || repo,
            project: d.project.key.clone(),
        })))
    }
}

impl Graph {
    /// The demo data as the bootstrap loads it (`devseed.Demo` of `GOAP_GRAPH_SEED=demo`), as the graph service:
    /// whether it wrote anything.
    pub(super) async fn load_demo(&self) -> Result<bool, String> {
        let d: Demo = serde_json::from_str(DEMO_JSON).map_err(|e| format!("demo data: {e}"))?;
        let system = Caller {
            subject: "system:graph".into(),
            on_behalf_of: String::new(),
            at_least: Vec::new(),
            project: String::new(),
            command: String::new(),
            roles: vec![ROLE_ADMIN.into()],
            org: String::new(),
        };
        let org = self
            .write_on(system.clone(), &[], async |tx, acc| {
                demo_organisation(tx, acc, &d).await
            })
            .await
            .map_err(|e| e.message().to_string())?
            .into_inner();
        let repo = self
            .write_on(system, &[], async |tx, acc| demo_repository(tx, acc, &d).await)
            .await
            .map_err(|e| e.message().to_string())?
            .into_inner();
        Ok(org || repo)
    }
}

/// The person a call acts for: the caller, or the person a service acts for.
fn end_user_of(c: &Caller) -> &str {
    if c.subject.starts_with("system:") {
        &c.on_behalf_of
    } else {
        &c.subject
    }
}

/// The units of the demo organisation under the root unit, the demo project under the root project and the
/// assignment binding them, what is missing of them, in one change (`demoOrganisation`).
async fn demo_organisation(tx: &mut Tx<'_, Session>, acc: &Access, d: &Demo) -> Result<bool, GraphError> {
    let has_org = by_key(tx, ORGANISATION, ACME).await?.is_some();
    let has_project = by_key(tx, ORGANISATION, &d.project.key).await?.is_some();
    if has_org && has_project {
        return Ok(false);
    }
    let mut edits = Vec::new();
    if !has_org {
        let root = root(tx, STRUCTURE_ORGANISATION).await?;
        for u in &d.units {
            let mut e = seed_node(&u.key, ORG_UNIT, object(json!({"name": u.name, "kind": u.kind})));
            if u.parent.is_empty() {
                link_to(&mut e, PART_OF, root.clone());
            } else {
                link_key(&mut e, PART_OF, &u.parent);
            }
            edits.push(e);
        }
    }
    if !has_project {
        let root = root(tx, STRUCTURE_PROJECT).await?;
        let p = &d.project;
        let mut props = Map::new();
        for (k, v) in [
            ("name", &p.name),
            ("description", &p.description),
            ("kind", &p.kind),
            ("status", &p.status),
        ] {
            if !v.is_empty() {
                props.insert(k.into(), Json::String(v.clone()));
            }
        }
        if !p.methodologies.is_empty() {
            props.insert("methodologies".into(), json!(p.methodologies));
        }
        let mut project = seed_node(&p.key, PROJECT_UNIT, props);
        link_to(&mut project, PROJECT_PART_OF, root);
        edits.push(project);
        let mut props = Map::new();
        if !d.assignment.description.is_empty() {
            props.insert("description".into(), Json::String(d.assignment.description.clone()));
        }
        if !d.assignment.roles.is_empty() {
            props.insert("roles".into(), json!(d.assignment.roles));
        }
        let mut asg = seed_node(&format!("ASG:{ACME}/{}", p.key), ASSIGNMENT, props);
        if has_org {
            let acme = by_key(tx, ORGANISATION, ACME)
                .await?
                .ok_or_else(|| GraphError::not_found(format!("node key {ACME:?}")))?;
            link_to(&mut asg, ASSIGNS_ORG, acme.node_ref());
        } else {
            link_key(&mut asg, ASSIGNS_ORG, ACME);
        }
        link_key(&mut asg, ASSIGNS_PROJECT, &p.key);
        edits.push(asg);
    }
    let title = "Import demo organisation";
    let project_id = tx.structure_root(STRUCTURE_PROJECT)?;
    let r = tx
        .commit_edits(
            Commit {
                namespace: ORGANISATION.into(),
                title: title.into(),
                intent: title.into(),
                by: "graphsvc.seed".into(),
                baseline_name: title.into(),
                edits,
                project_id,
                ..Commit::default()
            },
            &|node: &str, key: &str, t: &Transition| acc.transition(node, key, t),
        )
        .await;
    match r {
        Ok(_) => Ok(true),
        // the graph holds what the seed states already (`SeedChange`)
        Err(e) if e.message.contains(graph_core::write::commit::NOTHING_TO_COMMIT) => Ok(true),
        Err(e) => Err(e),
    }
}

/// The ALM data of the demo in the demo project, owned by the units of the demo organisation, in one change, once
/// (`Demo`: nothing when NEED-1 is there; `demoRepository`).
async fn demo_repository(tx: &mut Tx<'_, Session>, acc: &Access, d: &Demo) -> Result<bool, GraphError> {
    if by_key(tx, ALM, "NEED-1").await?.is_some() {
        return Ok(false);
    }
    let mut edits: Vec<NodeEdit> = d
        .nodes
        .iter()
        .map(|n| {
            let mut e = seed_node(&n.key, &n.typ, n.properties.clone());
            e.owner = d.owners.get(&n.key).cloned().unwrap_or_default();
            e.state = n.state.clone();
            e
        })
        .collect();
    for [from, typ, to] in &d.links {
        let Some(i) = d.nodes.iter().position(|n| &n.key == from) else {
            return Err(GraphError::internal(format!("demo link from unknown node {from}")));
        };
        link_key(&mut edits[i], &format!("{ALM}@{typ}"), to);
    }
    tx.commit_edits(
        Commit {
            namespace: ALM.into(),
            title: "Import demo data".into(),
            intent: "Seed demo data".into(),
            by: "devseed".into(),
            baseline_name: "Initial baseline".into(),
            edits,
            project_id: d.project.key.clone(),
            ..Commit::default()
        },
        &|node: &str, key: &str, t: &Transition| acc.transition(node, key, t),
    )
    .await?;
    Ok(true)
}
