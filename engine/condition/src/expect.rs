//! Expectations: the expected outcome of an action as a pattern on the domain axis.
use serde::{Deserialize, Serialize};

use crate::{condition::Error, unparse::go_quote};

/// Expectation is the expected outcome of an action, expressed as a pattern on the domain axis. It compiles to a CEL
/// condition so that the expected link is at the same time the action specification, its success criterion and a
/// plannable effect.
///
/// For each change impact x of the change (ADR 0024) that modifies a node (filtered by where), the change must
/// contain a change impact whose node was written, either:
///   - produce.op == "update_node": a new version of x's node;
///   - produce.op == "create_node": a created node of type produce.nodeType linked to x's node by link.type
///     (direction "out": new -> x, "in": x -> new).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Expectation {
    /// changeImpacts
    #[serde(rename = "forEach", default)]
    pub for_each: String,
    #[serde(rename = "where", default, skip_serializing_if = "String::is_empty")]
    pub r#where: String,
    #[serde(default)]
    pub produce: ProduceSpec,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub link: Option<LinkSpec>,
}

/// ProduceSpec describes the produced node.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProduceSpec {
    /// create_node | update_node
    #[serde(default)]
    pub op: String,
    #[serde(rename = "nodeType", default, skip_serializing_if = "String::is_empty")]
    pub node_type: String,
}

/// LinkSpec describes the expected link between the produced node and x.target.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct LinkSpec {
    #[serde(rename = "type", default)]
    pub r#type: String,
    /// out (default) | in
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub direction: String,
}

impl Expectation {
    /// Expr compiles the expectation into a CEL expression. The iteration variable in where is `x`.
    pub fn expr(&self) -> Result<String, Error> {
        if self.for_each != "changeImpacts" {
            return Err(Error::Compile(format!(
                "expects.forEach must be changeImpacts, got {}",
                go_quote(&self.for_each)
            )));
        }
        // what is expected of a node the change modifies: created nodes are not concerned
        let mut wh = r#"x.intent == "modified""#.to_string();
        if !self.r#where.trim().is_empty() {
            wh.push_str(&format!(" && ({})", self.r#where));
        }
        let src = format!("changeImpacts.filter(x, {wh})");
        let m = self.change_impact_match()?;
        // An expectation over an empty selection is vacuously true; require at least one element so the effect is
        // meaningful for the planner.
        Ok(format!("size({src}) > 0 && {src}.all(x, {m})"))
    }

    /// changeImpactMatch is the match of an expectation over change impacts: what the change must contain for each x
    /// (a change impact with a pre version).
    fn change_impact_match(&self) -> Result<String, Error> {
        match self.produce.op.as_str() {
            "update_node" => Ok(
                r#"changeImpacts.exists(n, n.intent == "modified" && n.hasPost && n.key == x.key)"#
                    .to_string(),
            ),
            "create_node" => {
                let Some(link) = self.link.as_ref().filter(|l| !l.r#type.is_empty()) else {
                    return Err(Error::Compile(
                        "expects: create_node requires link.type".to_string(),
                    ));
                };
                let type_check = if self.produce.node_type.is_empty() {
                    String::new()
                } else {
                    format!(" && n.type == {}", go_quote(&self.produce.node_type))
                };
                // "out": the new node links to x, "in": x links to the new node
                if link.direction == "in" {
                    return Ok(format!(
                        r#"changeImpacts.exists(n, n.intent == "created" && n.hasPost{} && x.hasPost && x.post.out.exists(l, l.type == {} && l.to.id == n.post.id))"#,
                        type_check,
                        go_quote(&link.r#type)
                    ));
                }
                Ok(format!(
                    r#"changeImpacts.exists(n, n.intent == "created" && n.hasPost{} && n.post.out.exists(l, l.type == {} && l.to.id == x.pre.id))"#,
                    type_check,
                    go_quote(&link.r#type)
                ))
            }
            op => Err(Error::Compile(format!(
                "expects.produce.op must be create_node or update_node, got {}",
                go_quote(op)
            ))),
        }
    }
}
