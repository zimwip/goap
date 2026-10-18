//! The directory, the authorizer, the session and the floor of the administrators over a graph in memory.

use std::sync::Arc;

use async_trait::async_trait;
use goap_access::*;
use goap_authz::{Authorizer as _, Principal, Request, Resource};
use goap_ctx::Ctx;
use goap_domain::{Baseline, BaselineID, Link, Node, NodeID, NodeRef, Structures};
use goap_graphapi::{Error, NodeValidator, ValidatorQuery};
use serde_json::{Map, json};

struct Fake {
    nodes: Vec<Node>,
    links: Vec<Link>,
    down: bool,
}

#[async_trait]
impl goap_graphsnap::Graph for Fake {
    async fn branch_head(&self, _: &Ctx, _: &str, _: &str) -> Result<Baseline, Error> {
        if self.down {
            return Err(Error::msg("graph down"));
        }
        Ok(Baseline {
            id: BaselineID::from("B1"),
            ..Baseline::default()
        })
    }
    async fn baseline_graph(
        &self,
        _: &Ctx,
        _: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), Error> {
        Ok((self.nodes.clone(), self.links.clone()))
    }
}

#[async_trait]
impl Graph for Fake {
    async fn structures(&self, _: &Ctx) -> Result<Structures, Error> {
        if self.down {
            return Err(Error::msg("graph down"));
        }
        Ok(goap_typecat::builtin().structures())
    }
}

#[async_trait]
impl ValidatorQuery for Fake {
    async fn nodes_of_type(&self, _: &Ctx, _: &str, typ: &str) -> Result<Vec<Node>, Error> {
        Ok(self
            .nodes
            .iter()
            .filter(|n| n.r#type == typ)
            .cloned()
            .collect())
    }
    async fn out_links_of(&self, _: &Ctx, r: &NodeRef) -> Result<Vec<Link>, Error> {
        Ok(self
            .links
            .iter()
            .filter(|l| l.from.id == r.id)
            .cloned()
            .collect())
    }
    async fn node(&self, _: &Ctx, r: &NodeRef) -> Result<Node, Error> {
        self.nodes
            .iter()
            .find(|n| n.id == r.id)
            .cloned()
            .ok_or_else(|| Error::not_found("node"))
    }
}

fn node(
    id: &str,
    key: &str,
    typ: &str,
    state: &str,
    props: Map<String, serde_json::Value>,
) -> Node {
    Node {
        id: NodeID::from(id),
        key: key.into(),
        r#type: typ.into(),
        namespace: "organisation".into(),
        state: state.into(),
        properties: props,
        ..Node::default()
    }
}

fn lnk(typ: &str, from: &str, to: &str) -> Link {
    Link {
        r#type: typ.into(),
        from: NodeRef::new(from, 0),
        to: NodeRef::new(to, 0),
        ..Link::default()
    }
}

fn organisation(admin_state: &str) -> Fake {
    let user = |s: &str| json!({"subject": s}).as_object().unwrap().clone();
    let roles = |r: &str| {
        Assignment {
            roles: vec![r.into()],
            ..Assignment::default()
        }
        .props()
    };
    Fake {
        nodes: vec![
            node("1", DEFAULT_ORG, NODE_TYPE_ORG_UNIT, "", Map::new()),
            node("2", DEFAULT_PROJECT, NODE_TYPE_PROJECT_UNIT, "", Map::new()),
            node("3", "USR:ada", NODE_TYPE_USER, admin_state, user("ada")),
            node("4", "USR:bob", NODE_TYPE_USER, "active", user("bob")),
            node(
                "5",
                &platform_assignment_key(DEFAULT_ORG),
                NODE_TYPE_ASSIGNMENT,
                "",
                roles(ROLE_READER),
            ),
            node(
                "6",
                &platform_assignment_key("USR:ada"),
                NODE_TYPE_ASSIGNMENT,
                "",
                roles(ROLE_ADMIN),
            ),
        ],
        links: vec![
            lnk(LINK_PROJECT_PART_OF, "2", "2"),
            lnk(LINK_MEMBER_OF, "3", "1"),
            lnk(LINK_MEMBER_OF, "4", "1"),
            lnk(LINK_ASSIGNS_ORG, "5", "1"),
            lnk(LINK_ASSIGNS_ORG, "6", "3"),
        ],
        down: false,
    }
}

fn principal(subject: &str) -> Principal {
    Principal {
        subject: subject.into(),
        ..Principal::default()
    }
}

#[tokio::test]
async fn directory_authorizer_and_session() {
    let ctx = Ctx::background();
    let dir = Arc::new(Directory::new(Arc::new(organisation("active"))));
    let snap = dir.snapshot(&ctx).await.unwrap();
    assert_eq!(snap.baseline, BaselineID::from("B1"));
    assert_eq!(snap.users().len(), 2);
    let ada = dir.enrich(&ctx, &principal("ada")).await;
    assert_eq!(ada.org, DEFAULT_ORG);
    assert!(
        ada.roles.contains(&ROLE_ADMIN.to_string()) && ada.roles.contains(&ROLE_READER.to_string())
    );
    assert!(
        dir.may_access_project(&ctx, &principal("bob"), DEFAULT_PROJECT)
            .await
            .unwrap()
    );

    let a = Authorizer::new(dir.clone()).unwrap();
    let req = |p: &str, action: &str, typ: &str| Request {
        subject: principal(p),
        action: action.into(),
        resource: Resource {
            r#type: typ.into(),
            ..Resource::default()
        },
    };
    assert!(
        a.authorize(&ctx, &req("ada", "write", "policy"))
            .await
            .unwrap(),
        "the floor admits an administrator"
    );
    assert!(
        !a.authorize(&ctx, &req("bob", "write", "policy"))
            .await
            .unwrap()
    );
    assert!(
        !a.authorize(&ctx, &req("", "read", "change")).await.unwrap(),
        "anonymous is refused"
    );
    assert!(
        a.floor()
            .authorize(&ctx, &req("ada", "admin", "platform"))
            .await
            .unwrap()
    );
    assert!(
        !a.floor()
            .authorize(&ctx, &req("bob", "admin", "platform"))
            .await
            .unwrap()
    );

    let s = a.session(&ctx, &principal("ada")).await.unwrap();
    assert!(s.can.administer && s.can.approve);
    let v = serde_json::to_value(&s).unwrap();
    assert_eq!(v["subject"], "ada");
    assert_eq!(v["names"]["types"]["orgUnit"], NODE_TYPE_ORG_UNIT);
    assert_eq!(v["names"]["keys"]["platformScope"], PLATFORM_SCOPE);
    assert_eq!(v["platformRoles"][0]["name"], ROLE_ADMIN);
    assert_eq!(v["structures"][0]["root"], DEFAULT_ORG);
    let bob = a.session(&ctx, &principal("bob")).await.unwrap();
    assert!(
        !bob.can.administer && bob.can.approve,
        "a reader works everywhere"
    );

    let down = Arc::new(Directory::new(Arc::new(Fake {
        down: true,
        ..organisation("active")
    })));
    let err = Authorizer::new(down.clone())
        .unwrap()
        .session(&ctx, &principal("ada"))
        .await
        .unwrap_err();
    assert!(err.is_no_session());
    assert_eq!(
        err.to_string(),
        "session: the organisation cannot be read\ngraph down"
    );
    assert_eq!(down.enrich(&ctx, &principal("ada")).await, principal("ada"));
    // the criticality facet reads the last snapshot only: the compiled-in table before any
    let facet = down.criticality_facet();
    let v = facet(&goap_domain::Change::default(), chrono::Utc::now());
    let p = v.downcast_ref::<goap_criticality::Policy>().unwrap();
    assert_eq!(*p, goap_criticality::defaults()[&goap_criticality::DEFAULT]);
}

/// A change may not leave the graph without an active administrator.
#[tokio::test]
async fn admin_floor_validator() {
    let ctx = Ctx::background();
    let v = AdminFloorValidator;
    assert_eq!(v.types(), [NODE_TYPE_USER, NODE_TYPE_ASSIGNMENT]);
    v.validate(&ctx, &organisation("active"), &[])
        .await
        .unwrap();
    let err = v
        .validate(&ctx, &organisation("inactive"), &[])
        .await
        .unwrap_err();
    assert!(err.is_invalid());
    assert_eq!(
        err.to_string(),
        "this change would leave no active administrator: invalid"
    );
}

#[test]
fn keys_and_props() {
    let p = goap_authz::Policy::new("true", "change", "read", "allow");
    assert!(
        policy_key(&p).starts_with("POL:change/read/")
            && policy_key(&p).len() == "POL:change/read/".len() + 8
    );
    assert_eq!(policy_from_props(&policy_props(&p)).unwrap(), p);
    assert_eq!(assignment_key("U", "P"), "ASG:U/P");
    assert_eq!(platform_assignment_key("U"), "ASG:U/PLATFORM");
    assert_eq!(role_key("admin"), "ROLE:admin");
    assert_eq!(*PLATFORM_ROLES, ["admin", "reader"]);
    let pu = ProjectUnit {
        name: "A".into(),
        methodologies: vec!["sdlc".into()],
        ..ProjectUnit::default()
    };
    assert_eq!(project_from_props(&pu.props()).unwrap(), pu);
    let bad = json!({"roles": "admin"}).as_object().unwrap().clone();
    assert_eq!(
        assignment_from_props(&bad).unwrap_err().to_string(),
        "assignment: roles must be a list of strings"
    );
    assert!(is_waiting_unit(
        json!({"waiting": true}).as_object().unwrap()
    ));
    assert_eq!(
        serde_json::to_value(User {
            subject: "a".into(),
            unit: "U".into(),
            ..User::default()
        })
        .unwrap(),
        json!({"subject": "a"})
    );
}
