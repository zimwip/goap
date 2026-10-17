//! The algorithms plugged in the domain (ADR 0018), the attribute and
//! node validators of a node type and the validators of a link type, and the guards and actions of lifecycle
//! transitions. Like the lifecycle they come from the type catalogue in force, resolved (instance parameters and
//! script). The guards and actions are free functions taking a [`TransitionEnv`] (the change and the impact of the
//! moved node).

use goap_algo::USAGE_NODE_VALIDATOR;
use goap_ctx::Ctx;
use goap_def::{ATTR_BOOLEAN, ATTR_DATE, ATTR_ENUM, ATTR_NUMBER, ATTR_STRING};
use goap_domain::{AttributeCheck, Change, ChangeImpact, Link, Node, NodeRef, Transition};
use goap_dsl::{AlgorithmInput, ChangeInfo, ImpactInfo, LinkInfo, TransitionInfo};
use serde_json::{Map, Value};

use crate::lifecycle::{TypeIndex, invalidf};
use crate::{Graph, Result};

/// dslNode is a node as an algorithm sees it.
pub(crate) fn dsl_node(n: &Node, props: &Map<String, Value>) -> goap_dsl::Node {
    goap_dsl::Node {
        id: n.id.to_string(),
        version: n.version,
        key: n.key.clone(),
        r#type: n.r#type.clone(),
        state: n.state.clone(),
        props: props.clone(),
    }
}

/// Go's `%v` of a value.
fn v(x: &Value) -> String {
    goap_dsl::sprint(std::slice::from_ref(x))
}

impl TypeIndex {
    /// checkAttributes checks that a node carries attributes of its type only (unless the type is open,
    /// `additionalProperties`), then the type and enum membership of their values: what every edit of a draft is held
    /// to (ADR 0076, 0079), a draft being otherwise free to be incomplete.
    pub(crate) fn check_attributes(&self, n: &Node, props: &Map<String, Value>) -> Result<()> {
        let Some(cat) = &self.cat else {
            return Ok(());
        };
        let (names, open) = cat.attribute_names(&n.r#type);
        if !open {
            let mut keys: Vec<&String> = props.keys().collect();
            keys.sort();
            for k in keys {
                if !names.contains(k) {
                    return Err(invalidf(format!(
                        "{} ({}): property {k:?} is not an attribute of {}",
                        n.key, n.r#type, n.r#type
                    )));
                }
            }
        }
        for a in cat.attribute_checks(&n.r#type) {
            if let Err(e) = check_attribute_value(&a, props.get(&a.name).unwrap_or(&Value::Null)) {
                return Err(invalidf(format!(
                    "{} ({}): property {:?} is invalid: {e}",
                    n.key, n.r#type, a.name
                )));
            }
        }
        Ok(())
    }

    /// checkLinkAttributes checks the type and enum membership of the properties of a link against the attributes of
    /// its link type.
    pub(crate) fn check_link_attributes(
        &self,
        typ: &str,
        props: &Map<String, Value>,
    ) -> Result<()> {
        let Some(cat) = &self.cat else {
            return Ok(());
        };
        for a in cat.link_attribute_checks(typ) {
            if let Err(e) = check_attribute_value(&a, props.get(&a.name).unwrap_or(&Value::Null)) {
                return Err(invalidf(format!(
                    "link {typ}: property {:?} is invalid: {e}",
                    a.name
                )));
            }
        }
        Ok(())
    }
}

/// nodeResolver resolves the node a link reaches as it will be (Go's `nodeResolver`): None when it cannot be
/// resolved.
#[async_trait::async_trait]
pub(crate) trait NodeResolver: Send {
    async fn resolve(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Option<Node>>;
}

impl Graph {
    /// validateProps checks the properties a draft is accepted with (an accepted review) and lands with (Apply): the
    /// type and enum membership of the values of its attributes, then the validators of its type (attribute
    /// validators and node validators). The first rejection is returned as an invalid error.
    pub(crate) async fn validate_props(
        &self,
        ctx: &Ctx,
        ix: &TypeIndex,
        n: &Node,
        props: &Map<String, Value>,
    ) -> Result<()> {
        ix.check_attributes(n, props)?;
        let vs = ix.validators_of(&n.r#type);
        if vs.is_empty() {
            return Ok(());
        }
        let view = dsl_node(n, props);
        for b in &vs {
            let out = goap_dsl::run_algorithm(
                ctx,
                b,
                AlgorithmInput {
                    node: view.clone(),
                    property: b.property.clone(),
                    value: props.get(&b.property).cloned().unwrap_or(Value::Null),
                    ..Default::default()
                },
            )
            .await;
            if b.r#type == USAGE_NODE_VALIDATOR {
                let out = out.map_err(|e| {
                    invalidf(format!(
                        "{} ({}): node validator {}: {e}",
                        n.key, n.r#type, b.instance
                    ))
                })?;
                if !out.ok() {
                    return Err(invalidf(format!(
                        "{} ({}) is invalid: {} (validator {})",
                        n.key, n.r#type, out.failures[0], b.instance
                    )));
                }
                continue;
            }
            let out = out.map_err(|e| {
                invalidf(format!(
                    "{} ({}): property {:?}: {e}",
                    n.key, n.r#type, b.property
                ))
            })?;
            if !out.ok() {
                return Err(invalidf(format!(
                    "{} ({}): property {:?} is invalid: {} (validator {})",
                    n.key, n.r#type, b.property, out.failures[0], b.instance
                )));
            }
        }
        Ok(())
    }

    /// runLinkValidators runs what the link type of l plugs: the property validators of its attributes (the value is
    /// the link property of that name, the node the one the link leaves), then its link validators (given the link and
    /// both of its ends). A target that cannot be resolved skips the link validators of that link. The first
    /// rejection is an invalid error.
    pub(crate) async fn run_link_validators(
        &self,
        ctx: &Ctx,
        ix: &TypeIndex,
        n: &Node,
        l: &Link,
        resolve: Option<&mut dyn NodeResolver>,
    ) -> Result<()> {
        let Some(cat) = &ix.cat else {
            return Ok(());
        };
        let avs = cat.link_attribute_validators(&l.r#type).to_vec();
        let lvs = cat.link_type_validators(&l.r#type).to_vec();
        if avs.is_empty() && lvs.is_empty() {
            return Ok(());
        }
        let from = dsl_node(n, &n.properties);
        for b in &avs {
            let out = goap_dsl::run_algorithm(
                ctx,
                b,
                AlgorithmInput {
                    node: from.clone(),
                    property: b.property.clone(),
                    value: l
                        .properties
                        .get(&b.property)
                        .cloned()
                        .unwrap_or(Value::Null),
                    ..Default::default()
                },
            )
            .await
            .map_err(|e| {
                invalidf(format!(
                    "{}: link {}: property {:?}: {e}",
                    n.key, l.r#type, b.property
                ))
            })?;
            if !out.ok() {
                return Err(invalidf(format!(
                    "{}: link {}: property {:?} is invalid: {} (validator {})",
                    n.key, l.r#type, b.property, out.failures[0], b.instance
                )));
            }
        }
        let Some(resolve) = resolve else {
            return Ok(());
        };
        if lvs.is_empty() {
            return Ok(());
        }
        let Some(target) = resolve.resolve(ctx, &l.to).await? else {
            return Ok(());
        };
        let input = AlgorithmInput {
            link: LinkInfo {
                r#type: l.r#type.clone(),
                props: l.properties.clone(),
            },
            from,
            to: dsl_node(&target, &target.properties),
            ..Default::default()
        };
        for b in &lvs {
            let out = goap_dsl::run_algorithm(ctx, b, input.clone())
                .await
                .map_err(|e| {
                    invalidf(format!(
                        "{}: link {} to {}: link validator {}: {e}",
                        n.key, l.r#type, target.key, b.instance
                    ))
                })?;
            if !out.ok() {
                return Err(invalidf(format!(
                    "{}: link {} to {} is invalid: {} (validator {})",
                    n.key, l.r#type, target.key, out.failures[0], b.instance
                )));
            }
        }
        Ok(())
    }
}

/// checkAttributeValue checks a value against the type of its attribute. A missing or empty value is accepted
/// (requiring one is the job of a validator), and so is any value of an attribute that names no type.
pub(crate) fn check_attribute_value(
    a: &AttributeCheck,
    x: &Value,
) -> std::result::Result<(), String> {
    if x.is_null() || x.as_str() == Some("") {
        return Ok(());
    }
    match a.r#type.as_str() {
        ATTR_NUMBER => match x {
            Value::Number(_) => Ok(()),
            Value::String(s) if s.parse::<f64>().is_ok() => Ok(()),
            _ => Err(format!("a number is expected, got {}", v(x))),
        },
        ATTR_BOOLEAN => match x {
            Value::Bool(_) => Ok(()),
            _ => Err(format!("a boolean is expected, got {}", v(x))),
        },
        ATTR_DATE => {
            if let Value::String(s) = x
                && (chrono::NaiveDate::parse_from_str(s, "%Y-%m-%d").is_ok()
                    || chrono::DateTime::parse_from_rfc3339(s).is_ok()
                    || chrono::NaiveDateTime::parse_from_str(s, "%Y-%m-%dT%H:%M:%S").is_ok())
            {
                return Ok(());
            }
            Err(format!(
                "a date (YYYY-MM-DD or RFC 3339) is expected, got {}",
                v(x)
            ))
        }
        ATTR_ENUM => {
            if let Value::String(s) = x
                && a.values.contains(s)
            {
                return Ok(());
            }
            Err(format!("{} is not a value of the enum {}", v(x), a.r#enum))
        }
        ATTR_STRING => match x {
            Value::String(_) => Ok(()),
            _ => Err(format!("a string is expected, got {}", v(x))),
        },
        _ => Ok(()),
    }
}

/// What a guard or an action of a transition is run in (Go reads it from its applier): the change and the impact of
/// the moved node.
pub(crate) struct TransitionEnv<'a> {
    pub(crate) change: &'a Change,
    pub(crate) impact: Option<&'a ChangeImpact>,
    pub(crate) ix: &'a TypeIndex,
}

impl TransitionEnv<'_> {
    pub(crate) fn change_info(&self) -> ChangeInfo {
        ChangeInfo {
            id: self.change.id.to_string(),
            title: self.change.title.clone(),
            intent: self.change.intent.clone(),
            methodology: self.change.methodology.clone(),
            goal: self.change.goal.clone(),
        }
    }

    fn transition_info(t: &Transition) -> TransitionInfo {
        TransitionInfo {
            name: t.name.clone(),
            from: t.from.clone(),
            to: t.to.clone(),
        }
    }
}

/// childViews are the children of a document as an algorithm sees them.
pub(crate) fn child_views(children: &[Node]) -> Vec<goap_dsl::Node> {
    children
        .iter()
        .map(|c| dsl_node(c, &c.properties))
        .collect()
}

/// runGuards runs the guard algorithms of a transition: all must accept.
pub(crate) async fn run_guards(
    ctx: &Ctx,
    env: &TransitionEnv<'_>,
    n: &Node,
    t: &Transition,
    children: &[Node],
) -> Result<()> {
    let mut input = AlgorithmInput {
        node: dsl_node(n, &n.properties),
        children: child_views(children),
        change: env.change_info(),
        transition: TransitionEnv::transition_info(t),
        ..Default::default()
    };
    if let Some(cn) = env.impact {
        input.impact = ImpactInfo {
            intent: cn.intent.to_string(),
            review: cn.review.to_string(),
        };
    }
    for g in &t.guard_algos {
        let out = goap_dsl::run_algorithm(ctx, g, input.clone())
            .await
            .map_err(|e| {
                invalidf(format!(
                    "guard {} of {} on {}: {e}",
                    g.instance, t.name, n.key
                ))
            })?;
        if !out.ok() {
            return Err(invalidf(format!(
                "{} ({}) cannot take {}: {} (guard {})",
                n.key, n.r#type, t.name, out.failures[0], g.instance
            )));
        }
    }
    Ok(())
}

/// runActions runs the action algorithms of a transition in order and returns the property changes they ask for, to
/// be written into the draft the transition moves (set, unset). The changed properties are validated again.
pub(crate) async fn run_actions(
    ctx: &Ctx,
    g: &Graph,
    env: &TransitionEnv<'_>,
    n: &Node,
    t: &Transition,
    children: &[Node],
) -> Result<(Map<String, Value>, Vec<String>)> {
    if t.action_algos.is_empty() {
        return Ok((Map::new(), Vec::new()));
    }
    let mut props = n.properties.clone();
    let mut changed = false;
    let mut set = Map::new();
    let mut unset = Vec::new();
    for act in &t.action_algos {
        let mut cur = n.clone();
        cur.properties = props.clone();
        let out = goap_dsl::run_algorithm(
            ctx,
            act,
            AlgorithmInput {
                node: dsl_node(&cur, &props),
                children: child_views(children),
                change: env.change_info(),
                transition: TransitionEnv::transition_info(t),
                ..Default::default()
            },
        )
        .await
        .map_err(|e| {
            invalidf(format!(
                "action {} of {} on {}: {e}",
                act.instance, t.name, n.key
            ))
        })?;
        if !out.ok() {
            return Err(invalidf(format!(
                "{} ({}): action {} of {} failed: {}",
                n.key, n.r#type, act.instance, t.name, out.failures[0]
            )));
        }
        if !out.set.is_empty() || !out.unset.is_empty() {
            for (k, val) in &out.set {
                props.insert(k.clone(), val.clone());
                set.insert(k.clone(), val.clone());
            }
            for k in &out.unset {
                props.remove(k);
                set.remove(k);
                unset.push(k.clone());
            }
            changed = true;
        }
    }
    if !changed {
        return Ok((Map::new(), Vec::new()));
    }
    g.validate_props(ctx, env.ix, n, &props)
        .await
        .map_err(|e| e.context(format!("after the actions of {}", t.name)))?;
    Ok((set, unset))
}
