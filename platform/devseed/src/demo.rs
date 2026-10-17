//! Port of `demo.go`: the ALM demo repository, its organisation and its project.

use std::collections::HashMap;
use std::sync::Arc;

use goap_access as access;
use goap_ctx::Ctx;
use goap_domain::{NodeRef, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};
use goap_graph::{Commit, Error, Graph, LinkEdit, NodeEdit, Result};
use goap_graphsvc::{BoxError, DemoFn, demo_fn, seed_change, seed_node};
use serde_json::{Map, Value, json};

/// alm is the namespace of the delivery domain (domains/alm.yaml).
const ALM: &str = "alm";

/// DemoProject is the project of the demo: the ALM data acts in it, and the demo organisation (ORG-ACME) holds the
/// roles of its methodology there.
pub const DEMO_PROJECT: &str = "PROJ-DEMO";

/// demoMethodologies apply to the demo project, demoRoles are the roles ORG-ACME holds on it (those of sdlc).
const DEMO_METHODOLOGIES: &[&str] = &["sdlc"];
const DEMO_ROLES: &[&str] = &[
    "product_owner",
    "business_analyst",
    "solution_architect",
    "tech_lead",
    "developer",
    "release_manager",
    "service_owner",
    "change_approver",
];

/// demoNode is a node of the demo repository.
struct DemoNode {
    key: &'static str,
    r#type: &'static str,
    properties: Value,
    /// State is the state a node of a lifecycle lands in: a node is created in the initial state, which no change
    /// leaves a node in if it is not landable.
    state: &'static str,
}

fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

fn strings(xs: &[&str]) -> Vec<String> {
    xs.iter().map(|s| s.to_string()).collect()
}

/// Demo loads a small ALM repository (namespace alm) once: needs, requirements and tests
/// (methodologies/examples/impact-analysis.yaml), and the functions, components, build artifacts, applications,
/// solution, data, interfaces and flows of methodologies/sdlc.yaml, with a small organisation owning them and the demo
/// project ([`DEMO_PROJECT`]) bound to that organisation by an assignment. Each part is seeded when it is missing, so a
/// graph holding an older demo gains the project. It reports whether it seeded anything. The graph is bootstrapped
/// first (a seed is an ordinary change, ADR 0054).
pub async fn demo(ctx: &Ctx, g: &Graph) -> Result<bool> {
    Box::pin(g.bootstrap(ctx)).await?;
    let seeded = Box::pin(demo_organisation(ctx, g)).await?;
    match g.node_by_key(ctx, ALM, "NEED-1").await {
        Ok(_) => return Ok(seeded),
        Err(e) if !e.is_not_found() => return Err(e),
        Err(_) => {}
    }
    Box::pin(demo_repository(ctx, g)).await?;
    Ok(true)
}

/// DemoLoader is [`demo`] as the graph handler takes it (`Handler::demo`, the "Load demo data" setting): whether it
/// wrote anything, and the key of the demo project. Once something is written the directory (when given) reads the
/// organisation again, so the demo project is offered at once.
pub fn demo_loader(d: Option<Arc<access::Directory>>) -> DemoFn {
    demo_fn(move |ctx, g| {
        let d = d.clone();
        Box::pin(async move {
            let loaded = demo(ctx, g).await.map_err(|e| Box::new(e) as BoxError)?;
            if loaded && let Some(d) = &d {
                d.refresh(ctx).await.map_err(|e| Box::new(e) as BoxError)?;
            }
            Ok((loaded, DEMO_PROJECT.to_string()))
        })
    })
}

/// exists tells whether the namespace holds a node of that key.
async fn exists(ctx: &Ctx, g: &Graph, namespace: &str, key: &str) -> Result<bool> {
    match g.node_by_key(ctx, namespace, key).await {
        Ok(_) => Ok(true),
        Err(e) if e.is_not_found() => Ok(false),
        Err(e) => Err(e),
    }
}

/// demoOrganisation seeds the units of the demo organisation under the root unit, and the demo project under the root
/// project with the assignment binding the organisation to it, in one change of the organisation namespace (what is
/// missing of them).
async fn demo_organisation(ctx: &Ctx, g: &Graph) -> Result<bool> {
    let has_org = exists(ctx, g, access::NAMESPACE_ORGANISATION, "ORG-ACME").await?;
    let has_project = exists(ctx, g, access::NAMESPACE_ORGANISATION, DEMO_PROJECT).await?;
    if has_org && has_project {
        return Ok(false);
    }
    // the organisation first, under the root unit of the bootstrap (ADR 0054): the nodes below are owned by its units.
    // All the units are one change; a unit links to its parent, created by the same change (ToKey) or the root.
    let mut edits = Vec::new();
    if !has_org {
        let root = g
            .node_by_key(
                ctx,
                access::NAMESPACE_ORGANISATION,
                &g.structure(STRUCTURE_ORGANISATION).root,
            )
            .await?;
        for u in [
            ["ORG-ACME", "Acme", "company", ""],
            ["ORG-DIGITAL", "Direction Digitale", "direction", "ORG-ACME"],
            ["ORG-CHECKOUT", "Team Checkout", "team", "ORG-DIGITAL"],
            ["ORG-CRM", "Team CRM", "team", "ORG-DIGITAL"],
            ["ORG-FINANCE", "Team Finance", "team", "ORG-DIGITAL"],
            ["ORG-SECURITY", "Team Security", "team", "ORG-DIGITAL"],
        ] {
            let mut e = seed_node(
                u[0],
                access::NODE_TYPE_ORG_UNIT,
                obj(json!({"name": u[1], "kind": u[2]})),
            );
            if u[3].is_empty() {
                e = link_to(e, access::LINK_PART_OF, root.r#ref());
            } else {
                e.links.push(LinkEdit {
                    r#type: access::LINK_PART_OF.into(),
                    to_key: u[3].into(),
                    ..Default::default()
                });
            }
            edits.push(e);
        }
    }
    if !has_project {
        // the project hangs under the root project (ADR 0039); the assignment is where it meets the organisation
        let root = g
            .node_by_key(
                ctx,
                access::NAMESPACE_ORGANISATION,
                &g.structure(STRUCTURE_PROJECT).root,
            )
            .await?;
        let project = seed_node(
            DEMO_PROJECT,
            access::NODE_TYPE_PROJECT_UNIT,
            access::ProjectUnit {
                name: "Online commerce".into(),
                description: "Demo project: the delivery of Acme's online commerce solution".into(),
                kind: "product".into(),
                status: "active".into(),
                methodologies: strings(DEMO_METHODOLOGIES),
            }
            .props(),
        );
        edits.push(link_to(project, access::LINK_PROJECT_PART_OF, root.r#ref()));
        let mut asg = seed_node(
            &access::assignment_key("ORG-ACME", DEMO_PROJECT),
            access::NODE_TYPE_ASSIGNMENT,
            access::Assignment {
                roles: strings(DEMO_ROLES),
                description: "The demo organisation works on the demo project".into(),
            }
            .props(),
        );
        if has_org {
            let acme = g
                .node_by_key(ctx, access::NAMESPACE_ORGANISATION, "ORG-ACME")
                .await?;
            asg = link_to(asg, access::LINK_ASSIGNS_ORG, acme.r#ref());
        } else {
            asg.links.push(LinkEdit {
                r#type: access::LINK_ASSIGNS_ORG.into(),
                to_key: "ORG-ACME".into(),
                ..Default::default()
            });
        }
        asg.links.push(LinkEdit {
            r#type: access::LINK_ASSIGNS_PROJECT.into(),
            to_key: DEMO_PROJECT.into(),
            ..Default::default()
        });
        edits.push(asg);
    }
    seed_change(
        ctx,
        g,
        access::NAMESPACE_ORGANISATION,
        "Import demo organisation",
        edits,
    )
    .await?;
    Ok(true)
}

/// The nodes of the demo repository.
fn demo_nodes() -> Vec<DemoNode> {
    let n = |key, r#type, properties| DemoNode {
        key,
        r#type,
        properties,
        state: "",
    };
    let s = |key, r#type, properties, state| DemoNode {
        key,
        r#type,
        properties,
        state,
    };
    vec![
        n(
            "NEED-1",
            "alm@Need",
            json!({"title": "Pay for orders online"}),
        ),
        n(
            "NEED-2",
            "alm@Need",
            json!({"title": "Be refunded quickly"}),
        ),
        s(
            "REQ-1",
            "alm@Requirement",
            json!({"title": "Card payment goes through the Acme PSP (API v1)", "priority": "high"}),
            "proposed",
        ),
        s(
            "REQ-2",
            "alm@Requirement",
            json!({"title": "The refund is initiated within 24h via the PSP", "priority": "medium"}),
            "proposed",
        ),
        s(
            "REQ-3",
            "alm@Requirement",
            json!({"title": "Receipts are sent by email", "priority": "low"}),
            "proposed",
        ),
        n(
            "TST-1",
            "alm@TestCase",
            json!({"title": "Nominal card payment"}),
        ),
        n("TST-2", "alm@TestCase", json!({"title": "Full refund"})),
        n(
            "TST-3",
            "alm@TestCase",
            json!({"title": "Receipt received"}),
        ),
        s(
            "REQ-4",
            "alm@SecurityRequirement",
            json!({"title": "Card data is never stored in the clear (PCI DSS)", "priority": "high"}),
            "proposed",
        ),
        n(
            "CMP-1",
            "alm@Component",
            json!({"title": "payment-service", "technology": "java", "version": "1.4.2"}),
        ),
        n(
            "CMP-2",
            "alm@Component",
            json!({"title": "notification-service", "technology": "go", "version": "2.1.0"}),
        ),
        n(
            "CMP-3",
            "alm@Component",
            json!({"title": "settlement-batch", "technology": "shell", "version": "0.9.3"}),
        ),
        n(
            "CMP-4",
            "alm@Component",
            json!({"title": "card-crypto", "technology": "c", "version": "3.0.1"}),
        ),
        // ALM chain (alm)
        n(
            "FCT-1",
            "alm@Function",
            json!({"title": "Collect a payment"}),
        ),
        n("FCT-2", "alm@Function", json!({"title": "Refund an order"})),
        n(
            "FCT-3",
            "alm@Function",
            json!({"title": "Notify the customer"}),
        ),
        n(
            "FCT-4",
            "alm@Function",
            json!({"title": "Protect card data"}),
        ),
        n(
            "ART-1",
            "alm@BuildArtifact",
            json!({"name": "payment-service", "format": "jar", "version": "1.4.2", "coordinates": "com.acme:payment-service:1.4.2"}),
        ),
        n(
            "ART-2",
            "alm@BuildArtifact",
            json!({"name": "notification-service", "format": "tar.gz", "version": "2.1.0"}),
        ),
        n(
            "ART-3",
            "alm@BuildArtifact",
            json!({"name": "settlement-batch", "format": "tar.gz", "version": "0.9.3"}),
        ),
        n(
            "ART-4",
            "alm@BuildArtifact",
            json!({"name": "card-crypto", "format": "elf", "version": "3.0.1"}),
        ),
        n(
            "APP-1",
            "alm@Application",
            json!({"title": "Checkout", "version": "5.2"}),
        ),
        n(
            "APP-2",
            "alm@Application",
            json!({"title": "CRM", "version": "3.0"}),
        ),
        n(
            "APP-3",
            "alm@Application",
            json!({"title": "Finance back office", "version": "1.7"}),
        ),
        n("SOL-1", "alm@Solution", json!({"title": "Online commerce"})),
        n(
            "DAT-1",
            "alm@Data",
            json!({"title": "Order", "classification": "internal"}),
        ),
        n(
            "DAT-2",
            "alm@Data",
            json!({"title": "Payment", "classification": "confidential"}),
        ),
        n(
            "DAT-3",
            "alm@Data",
            json!({"title": "Customer", "classification": "personal"}),
        ),
        n(
            "ITF-1",
            "alm@Interface",
            json!({"title": "Payments API", "protocol": "REST", "version": "v1"}),
        ),
        n(
            "ITF-2",
            "alm@Interface",
            json!({"title": "Notification events", "protocol": "Kafka", "version": "v2"}),
        ),
        n(
            "ITF-3",
            "alm@Interface",
            json!({"title": "Settlement file", "protocol": "SFTP", "version": "v1"}),
        ),
        n(
            "FLW-1",
            "alm@Flow",
            json!({"title": "Payment confirmation", "mode": "asynchronous", "frequency": "real time"}),
        ),
        n(
            "FLW-2",
            "alm@Flow",
            json!({"title": "Daily settlement", "mode": "batch", "frequency": "daily"}),
        ),
        // environments and releases
        n(
            "ENV-DEV",
            "alm@Environment",
            json!({"name": "Development", "stage": "dev", "order": 1}),
        ),
        n(
            "ENV-TEST",
            "alm@Environment",
            json!({"name": "Integration", "stage": "test", "order": 2}),
        ),
        n(
            "ENV-STG",
            "alm@Environment",
            json!({"name": "Staging", "stage": "staging", "order": 3}),
        ),
        n(
            "ENV-PRD",
            "alm@Environment",
            json!({"name": "Production", "stage": "prod", "order": 4}),
        ),
        s(
            "REL-APP-1-5.2",
            "alm@Release",
            json!({"title": "Checkout 5.2", "version": "5.2", "status": "deployed"}),
            "candidate",
        ),
        n(
            "DEP-REL-APP-1-5.2-ENV-PRD",
            "alm@Deployment",
            json!({"status": "succeeded", "stage": "prod"}),
        ),
    ]
}

/// The links of the demo repository: source, link type (of alm), target.
const DEMO_LINKS: &[[&str; 3]] = &[
    ["REQ-1", "satisfies", "NEED-1"],
    ["REQ-2", "satisfies", "NEED-2"],
    ["REQ-3", "satisfies", "NEED-1"],
    ["TST-1", "verifies", "REQ-1"],
    ["TST-2", "verifies", "REQ-2"],
    ["TST-3", "verifies", "REQ-3"],
    // ALM chain: requirement ← function ← component ← artifact / application ← solution
    ["REQ-4", "satisfies", "NEED-1"],
    ["FCT-1", "realizes", "REQ-1"],
    ["FCT-2", "realizes", "REQ-2"],
    ["FCT-3", "realizes", "REQ-3"],
    ["FCT-4", "realizes", "REQ-4"],
    ["CMP-1", "implements", "FCT-1"],
    ["CMP-1", "implements", "FCT-2"],
    ["CMP-2", "implements", "FCT-3"],
    ["CMP-3", "implements", "FCT-2"],
    ["CMP-4", "implements", "FCT-4"],
    ["CMP-1", "depends_on", "CMP-4"],
    ["CMP-1", "accesses", "DAT-2"],
    ["CMP-3", "accesses", "DAT-2"],
    ["CMP-2", "accesses", "DAT-3"],
    ["ART-1", "built_from", "CMP-1"],
    ["ART-2", "built_from", "CMP-2"],
    ["ART-3", "built_from", "CMP-3"],
    ["ART-4", "built_from", "CMP-4"],
    ["APP-1", "composed_of", "CMP-1"],
    ["APP-1", "composed_of", "CMP-4"],
    ["APP-2", "composed_of", "CMP-2"],
    ["APP-3", "composed_of", "CMP-3"],
    ["APP-1", "deploys", "ART-1"],
    ["APP-1", "deploys", "ART-4"],
    ["APP-2", "deploys", "ART-2"],
    ["APP-3", "deploys", "ART-3"],
    ["SOL-1", "includes", "APP-1"],
    ["SOL-1", "includes", "APP-2"],
    ["SOL-1", "includes", "APP-3"],
    ["SOL-1", "addresses", "NEED-1"],
    ["SOL-1", "addresses", "NEED-2"],
    ["DAT-1", "owned_by", "APP-1"],
    ["DAT-2", "owned_by", "APP-1"],
    ["DAT-3", "owned_by", "APP-2"],
    ["ITF-1", "exposed_by", "APP-1"],
    ["ITF-1", "exchanges", "DAT-2"],
    ["ITF-2", "exposed_by", "APP-2"],
    ["ITF-2", "exchanges", "DAT-3"],
    ["ITF-3", "exposed_by", "APP-3"],
    ["ITF-3", "exchanges", "DAT-2"],
    ["FLW-1", "source", "APP-1"],
    ["FLW-1", "target", "APP-2"],
    ["FLW-1", "through", "ITF-2"],
    ["FLW-1", "carries", "DAT-3"],
    ["FLW-2", "source", "APP-1"],
    ["FLW-2", "target", "APP-3"],
    ["FLW-2", "through", "ITF-3"],
    ["FLW-2", "carries", "DAT-2"],
    ["ENV-DEV", "promotes_to", "ENV-TEST"],
    ["ENV-TEST", "promotes_to", "ENV-STG"],
    ["ENV-STG", "promotes_to", "ENV-PRD"],
    ["REL-APP-1-5.2", "releases", "APP-1"],
    ["REL-APP-1-5.2", "contains", "ART-1"],
    ["REL-APP-1-5.2", "contains", "ART-4"],
    ["DEP-REL-APP-1-5.2-ENV-PRD", "of_release", "REL-APP-1-5.2"],
    ["DEP-REL-APP-1-5.2-ENV-PRD", "in_environment", "ENV-PRD"],
];

/// demoRepository seeds the alm data of the demo in the demo project, owned by the units of the demo organisation.
async fn demo_repository(ctx: &Ctx, g: &Graph) -> Result<()> {
    // ownership: a node of one namespace is owned by a unit of the organisation (the owner of its versions)
    let owners: HashMap<&str, &str> = [
        ("CMP-1", "ORG-CHECKOUT"),
        ("CMP-2", "ORG-CRM"),
        ("CMP-3", "ORG-FINANCE"),
        ("CMP-4", "ORG-SECURITY"),
        ("APP-1", "ORG-CHECKOUT"),
        ("APP-2", "ORG-CRM"),
        ("APP-3", "ORG-FINANCE"),
        ("SOL-1", "ORG-DIGITAL"),
    ]
    .into_iter()
    .collect();
    let nodes = demo_nodes();
    // the nodes and their links are one change (a link belongs to the version of its source, created here)
    let mut edits: Vec<NodeEdit> = Vec::with_capacity(nodes.len());
    for n in &nodes {
        let mut e = seed_node(n.key, n.r#type, obj(n.properties.clone()));
        e.owner = owners.get(n.key).copied().unwrap_or_default().to_string();
        e.state = n.state.to_string();
        edits.push(e);
    }
    // the nodes of a change link each other, cycles included (an application is composed of components that
    // access data it owns): the links are part of the version of their source
    let at: HashMap<&str, usize> = nodes.iter().enumerate().map(|(i, n)| (n.key, i)).collect();
    for l in DEMO_LINKS {
        let Some(&i) = at.get(l[0]) else {
            return Err(Error::msg(format!("demo link from unknown node {}", l[0])));
        };
        edits[i].links.push(LinkEdit {
            r#type: format!("{ALM}@{}", l[1]),
            to_key: l[2].into(),
            ..Default::default()
        });
    }
    Box::pin(g.commit(
        ctx,
        Commit {
            namespace: ALM.into(),
            title: "Import demo data".into(),
            intent: "Seed demo data".into(),
            by: "devseed".into(),
            baseline_name: "Initial baseline".into(),
            edits,
            project_id: DEMO_PROJECT.into(),
            ..Default::default()
        },
    ))
    .await?;
    Ok(())
}

fn link_to(mut e: NodeEdit, typ: &str, to: NodeRef) -> NodeEdit {
    e.links.push(LinkEdit {
        r#type: typ.into(),
        to: Some(to),
        ..Default::default()
    });
    e
}
