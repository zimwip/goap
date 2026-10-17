//! Steps with foreach (ADR 0050, Go's `foreach.go`): the step is carried out once per element of the list its
//! expression gives, in parallel streams, each a sub-agent on the same change with `vars.item` bound.

use std::collections::{BTreeMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::Blackboard;
use goap_methodology::{Call, MethodChoice};
use serde_json::{Map, Value};

use crate::error::{Error, Result};
use crate::executors::{ActionContext, ActionResult};
use crate::gotemplate::TV;
use crate::process::STATUS_COMPLETED;
use crate::steps::StepContext;

/// FOREACH_SUFFIX separates the action from the element in the call key of a stream.
pub(crate) const FOREACH_SUFFIX: &str = "#step:";

/// run_foreach runs a call with foreach: the step waits for the first stream still at work and is retried when it
/// ends; it is done when every stream completed, and fails when one failed.
pub(crate) async fn run_foreach(
    ctx: &Ctx,
    ac: &ActionContext,
    step: &str,
    call: &Call,
) -> Result<ActionResult> {
    let Some(h) = ac.host.clone() else {
        return Err(Error::msg("a call needs a process on a change"));
    };
    let e = h.engine();
    let capability = call.capability.clone();
    let methodology_name = ac.process.methodology.clone();
    let list = goap_condition::compile_list(&call.foreach)
        .map_err(|err| Error::msg(format!("step {step}: foreach: {err}")))?;
    let mut items = list
        .eval(&ac.blackboard)
        .map_err(|err| Error::msg(format!("step {step}: foreach: {err}")))?;
    if !call.group_by.is_empty() {
        items = group_items(&ac.blackboard, items, &call.group_by)
            .map_err(|err| Error::msg(format!("step {step}: groupBy: {err}")))?;
    }
    struct Stream {
        key: String,
        id: String,
        item: Value,
    }
    let prefix = format!("{}{FOREACH_SUFFIX}", ac.action.name);
    let mut streams = Vec::new();
    let mut seen = HashSet::new();
    for (i, it) in items.into_iter().enumerate() {
        let mut id = item_key(&it, i);
        while seen.contains(&id) {
            id += &format!("-{i}");
        }
        seen.insert(id.clone());
        streams.push(Stream {
            key: format!("{prefix}{id}"),
            id,
            item: it,
        });
    }
    // streams started before keep their element: the list may no longer give it
    for (key, child_id) in &ac.process.children {
        let Some(id) = key.strip_prefix(&prefix) else {
            continue;
        };
        if seen.contains(id) {
            continue;
        }
        let child = e
            .store
            .get(ctx, child_id)
            .await
            .map_err(|err| err.context(format!("step {step}")))?;
        let item = child
            .step
            .as_ref()
            .map(|s| s.item.clone())
            .unwrap_or(Value::Null);
        seen.insert(id.to_string());
        streams.push(Stream {
            key: key.clone(),
            id: id.to_string(),
            item,
        });
    }
    let actor = e.as_(ctx, &ac.process);
    let mut wait_for = String::new();
    let mut methods: Vec<String> = Vec::new();
    let mut failed = Vec::new();
    for st in &streams {
        let mut bb = ac.blackboard.clone();
        bb.vars.insert("item".into(), st.item.clone());
        let me = if let Some(id) = ac.process.children.get(&st.key) {
            // the method it was started for stays
            let child = e
                .store
                .get(ctx, id)
                .await
                .map_err(|err| err.context(format!("step {step}")))?;
            let m = e.methodology_of(ctx, &ac.process).await?;
            let method = child
                .step
                .as_ref()
                .map(|s| s.method.clone())
                .unwrap_or_default();
            if method.is_empty() {
                return Err(Error::msg(format!(
                    "step {step}: stream {} has no method",
                    st.id
                )));
            }
            let Some(meth) = m.method_by_name(&method) else {
                return Err(Error::msg(format!(
                    "step {step}: stream {}: unknown method {method:?}",
                    st.id
                )));
            };
            MethodChoice {
                method: meth.clone(),
                agent_goal: m.method_goal(&meth.name),
            }
        } else {
            e.choose_method(ctx, ac, &capability, &bb, false)
                .await
                .map_err(|err| err.context(format!("step {step} [{}]", st.id)))?
        };
        if !methods.contains(&me.name) {
            methods.push(me.name.clone());
        }
        let mut sc = StepContext::with_method(ac.step.as_ref(), &me.method);
        sc.item = st.item.clone();
        sc.item_key = st.id.clone();
        let mut intent = if ac.action.description.is_empty() {
            format!("step {step}")
        } else {
            ac.action.description.clone()
        };
        intent += &format!(" for {}", st.id);
        if !ac.blackboard.change.intent.is_empty() {
            intent += &format!("\n\n(change: {})", ac.blackboard.change.intent);
        }
        let res = e
            .run_child_step(
                &actor,
                &h,
                &st.key,
                &methodology_name,
                me.actor_agent(),
                &me.agent_goal,
                &intent,
                Some(sc),
            )
            .await;
        match res {
            Err(err) if err.is_suspended() => {
                if wait_for.is_empty() {
                    wait_for = h.waiting_on();
                }
            }
            Err(err) => return Err(err.context(format!("step {step} [{}]", st.id))),
            Ok(res) if res.status != STATUS_COMPLETED => {
                failed.push(format!(
                    "{} ({}, process {})",
                    st.id, res.status, res.process_id
                ));
            }
            Ok(_) => {}
        }
    }
    if !wait_for.is_empty() {
        return Ok(ActionResult {
            suspended: true,
            child: wait_for,
            method: methods.join(","),
            output: format!("step {step}: {} stream(s) at work", streams.len()),
            ..Default::default()
        });
    }
    if !failed.is_empty() {
        return Err(Error::msg(format!(
            "step {step}: stream(s) did not complete: {}",
            failed.join(", ")
        )));
    }
    Ok(ActionResult {
        method: methods.join(","),
        output: format!(
            "step {step} done for {} element(s) with {}",
            streams.len(),
            methods.join(", ")
        ),
        ..Default::default()
    })
}

/// item_key is the identity of an element of a foreach list: a string as is, a map by its key, id or name, else its
/// position.
pub(crate) fn item_key(item: &Value, i: usize) -> String {
    match item {
        Value::String(s) if !s.is_empty() => return s.clone(),
        Value::Object(m) => {
            for k in ["key", "id", "name"] {
                if let Some(Value::String(s)) = m.get(k)
                    && !s.is_empty()
                {
                    return s.clone();
                }
            }
        }
        _ => {}
    }
    format!("#{i}")
}

/// group_items groups the elements by the key the expression gives for each: one {key, items} per group, in the
/// order the keys first appear.
pub(crate) fn group_items(bb: &Blackboard, items: Vec<Value>, expr: &str) -> Result<Vec<Value>> {
    let by = goap_condition::compile_value(expr)?;
    let mut keys: Vec<String> = Vec::new();
    let mut groups: BTreeMap<String, Vec<Value>> = BTreeMap::new();
    for it in items {
        let mut b = bb.clone();
        b.vars.insert("item".into(), it.clone());
        let v = by.eval(&b)?;
        let k = TV::from_json(&v).print();
        if !groups.contains_key(&k) {
            keys.push(k.clone());
        }
        groups.entry(k).or_default().push(it);
    }
    Ok(keys
        .into_iter()
        .map(|k| {
            let items = groups.remove(&k).unwrap_or_default();
            let mut m = Map::new();
            m.insert("key".into(), Value::String(k));
            m.insert("items".into(), Value::Array(items));
            Value::Object(m)
        })
        .collect())
}
