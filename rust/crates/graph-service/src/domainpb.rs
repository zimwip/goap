//! A domain definition as the registry answers it (Go's `lib/domainpb.DomainToPB`): the JSON the registry stores (or
//! the YAML of a built-in domain, read as Go's `def.ParseDomain` reads it) mapped onto the proto JSON of
//! `registry.v1.Domain`, then decoded. What the protocol has no field for (a structure's bootstrap, the bound
//! algorithms of a transition) is left out, as the Go converter leaves it out.
use goap_proto::registry::v1 as pb;
use graph_catalog::{catalog, def};
use serde_json::{Map, Value, json};

type Obj = Map<String, Value>;

/// The definition of a domain as a `registry.v1.Domain` (status and times are the caller's).
pub fn domain(def: &Value) -> Result<pb::Domain, String> {
    let d = obj(def);
    let mut out = Obj::new();
    text(&mut out, d, "name", "name");
    text(&mut out, d, "version", "version");
    text(&mut out, d, "description", "description");
    list(&mut out, d, "nodeTypes", "nodeTypes", Some("name"), node_type);
    list(&mut out, d, "linkTypes", "linkTypes", None, link_type);
    list(&mut out, d, "enums", "enums", None, enumeration);
    list(&mut out, d, "lifecycles", "lifecycles", None, lifecycle);
    list(&mut out, d, "algorithms", "algorithms", None, algorithm);
    list(&mut out, d, "algorithmInstances", "algorithmInstances", None, instance);
    list(&mut out, d, "itemTypes", "itemTypes", None, |o| {
        let mut m = Obj::new();
        text(&mut m, o, "name", "name");
        text(&mut m, o, "description", "description");
        list(&mut m, o, "attributes", "attributes", Some("name"), attribute);
        m
    });
    list(&mut out, d, "importers", "importers", None, |o| {
        let mut m = Obj::new();
        for k in ["name", "label", "description"] {
            text(&mut m, o, k, k);
        }
        list(&mut m, o, "params", "params", Some("name"), attribute);
        m
    });
    serde_json::from_value(Value::Object(out)).map_err(|e| format!("domain to protocol: {e}"))
}

fn node_type(o: &Obj) -> Obj {
    let mut m = Obj::new();
    for k in ["name", "description", "extends", "lifecycle", "editor", "icon", "color"] {
        text(&mut m, o, k, k);
    }
    list(&mut m, o, "attributes", "attributes", Some("name"), attribute);
    if let Some(d) = o.get("document").and_then(Value::as_object) {
        let mut doc = Obj::new();
        texts(&mut doc, d, "contains", "contains");
        m.insert("document".into(), Value::Object(doc));
    }
    if let Some(b) = o.get("changeControlled").and_then(Value::as_bool) {
        m.insert("changeControlled".into(), Value::Bool(b));
    }
    texts(&mut m, o, "validators", "validators");
    list(&mut m, o, "search", "search", None, |s| {
        let mut p = Obj::new();
        text(&mut p, s, "property", "property");
        flag(&mut p, s, "text", "text");
        flag(&mut p, s, "facet", "facet");
        p
    });
    if let Some(t) = o.get("structure").and_then(Value::as_object) {
        let mut st = Obj::new();
        for k in ["kind", "parent", "root"] {
            text(&mut st, t, k, k);
        }
        flag(&mut st, t, "selfParent", "selfParent");
        m.insert("structure".into(), Value::Object(st));
    }
    list(&mut m, o, "requires", "requires", None, |r| {
        let mut p = Obj::new();
        text(&mut p, r, "link", "link");
        int(&mut p, r, "count", "count");
        p
    });
    flag(&mut m, o, "adminOnly", "adminOnly");
    flag(&mut m, o, "additionalProperties", "additionalProperties");
    if let Some(f @ Value::Object(form)) = o.get("form")
        && !form.is_empty()
    {
        m.insert("form".into(), f.clone());
    }
    m
}

fn attribute(o: &Obj) -> Obj {
    let mut m = Obj::new();
    for k in [
        "name",
        "label",
        "description",
        "type",
        "widget",
        "diff",
        "enum",
        "section",
        "tooltip",
    ] {
        text(&mut m, o, k, k);
    }
    text(&mut m, o, "default", "defaultValue");
    int(&mut m, o, "order", "order");
    flag(&mut m, o, "asName", "asName");
    texts(&mut m, o, "validators", "validators");
    m
}

fn link_type(o: &Obj) -> Obj {
    let mut m = Obj::new();
    for k in ["name", "description", "from", "to", "label", "inverseLabel"] {
        text(&mut m, o, k, k);
    }
    flag(&mut m, o, "compose", "compose");
    list(&mut m, o, "attributes", "attributes", Some("name"), attribute);
    texts(&mut m, o, "validators", "validators");
    m
}

fn enumeration(o: &Obj) -> Obj {
    let mut m = Obj::new();
    text(&mut m, o, "name", "name");
    text(&mut m, o, "description", "description");
    list(&mut m, o, "values", "values", Some("value"), |v| {
        let mut p = Obj::new();
        text(&mut p, v, "value", "value");
        text(&mut p, v, "label", "label");
        p
    });
    m
}

fn lifecycle(o: &Obj) -> Obj {
    let mut m = Obj::new();
    for k in ["name", "description", "initial"] {
        text(&mut m, o, k, k);
    }
    list(&mut m, o, "states", "states", None, |s| {
        let mut p = Obj::new();
        text(&mut p, s, "name", "name");
        text(&mut p, s, "description", "description");
        flag(&mut p, s, "notCommittable", "notCommittable");
        flag(&mut p, s, "final", "final");
        p
    });
    list(&mut m, o, "transitions", "transitions", None, |t| {
        let mut p = Obj::new();
        for k in ["name", "description", "from", "to", "permission", "guard"] {
            text(&mut p, t, k, k);
        }
        if let Some(r) = t.get("requires").and_then(Value::as_object) {
            texts(&mut p, r, "attributes", "requiresAttributes");
            texts(&mut p, r, "outgoingLinks", "requiresOutgoingLinks");
        }
        if let Some(c) = t.get("children").and_then(Value::as_object) {
            texts(&mut p, c, "states", "childrenStates");
        }
        texts(&mut p, t, "guards", "guards");
        texts(&mut p, t, "actions", "actions");
        for k in ["vetos", "objectives"] {
            list(&mut p, t, k, k, None, |c| {
                let mut q = Obj::new();
                for f in ["name", "expr", "description"] {
                    text(&mut q, c, f, f);
                }
                q
            });
        }
        p
    });
    m
}

fn algorithm(o: &Obj) -> Obj {
    let mut m = Obj::new();
    for k in ["name", "description", "type", "language", "code"] {
        text(&mut m, o, k, k);
    }
    list(&mut m, o, "params", "params", None, |p| {
        let mut q = Obj::new();
        for k in ["name", "type", "description"] {
            text(&mut q, p, k, k);
        }
        flag(&mut q, p, "required", "required");
        if let Some(v) = p.get("default").filter(|v| !v.is_null()) {
            q.insert("defaultValue".into(), v.clone());
        }
        texts(&mut q, p, "values", "values");
        q
    });
    m
}

fn instance(o: &Obj) -> Obj {
    let mut m = Obj::new();
    for k in ["name", "description", "algorithm"] {
        text(&mut m, o, k, k);
    }
    if let Some(v @ Value::Object(values)) = o.get("values")
        && !values.is_empty()
    {
        m.insert("values".into(), v.clone());
    }
    m
}

static EMPTY: std::sync::LazyLock<Obj> = std::sync::LazyLock::new(Obj::new);

fn obj(v: &Value) -> &Obj {
    v.as_object().unwrap_or(&EMPTY)
}

/// A string field: a scalar read as its text, as yaml.v3 decodes it into a Go string.
fn scalar(v: &Value) -> Option<String> {
    match v {
        Value::String(s) => Some(s.clone()),
        Value::Number(n) => Some(n.to_string()),
        Value::Bool(b) => Some(b.to_string()),
        _ => None,
    }
}

fn text(m: &mut Obj, o: &Obj, from: &str, to: &str) {
    if let Some(s) = o.get(from).and_then(scalar) {
        m.insert(to.into(), Value::String(s));
    }
}

fn texts(m: &mut Obj, o: &Obj, from: &str, to: &str) {
    if let Some(a) = o.get(from).and_then(Value::as_array) {
        m.insert(
            to.into(),
            Value::Array(a.iter().filter_map(scalar).map(Value::String).collect()),
        );
    }
}

fn flag(m: &mut Obj, o: &Obj, from: &str, to: &str) {
    if let Some(b) = o.get(from).and_then(Value::as_bool) {
        m.insert(to.into(), Value::Bool(b));
    }
}

fn int(m: &mut Obj, o: &Obj, from: &str, to: &str) {
    if let Some(n) = o.get(from).and_then(Value::as_i64) {
        m.insert(to.into(), json!(n));
    }
}

/// A list of objects, a plain scalar standing for one named by `short` where the list accepts it.
fn list(m: &mut Obj, o: &Obj, from: &str, to: &str, short: Option<&str>, f: impl Fn(&Obj) -> Obj) {
    let Some(a) = o.get(from).and_then(Value::as_array) else {
        return;
    };
    let items = a
        .iter()
        .map(|v| match (v, short) {
            (Value::Object(x), _) => Value::Object(f(x)),
            (v, Some(k)) => {
                let mut x = Obj::new();
                if let Some(s) = scalar(v) {
                    x.insert(k.into(), Value::String(s));
                }
                Value::Object(f(&x))
            }
            _ => Value::Object(f(&EMPTY)),
        })
        .collect();
    m.insert(to.into(), Value::Array(items));
}

/// The resolved attributes of a type (`domainpb.AttributeInfosToPB`).
pub fn attribute_infos(as_: &[catalog::Attribute]) -> Vec<pb::AttributeInfo> {
    as_.iter()
        .map(|a| {
            let d = &a.def;
            pb::AttributeInfo {
                attribute: Some(pb::Attribute {
                    name: d.name.clone(),
                    label: d.label.clone(),
                    description: d.description.clone(),
                    r#type: d.typ.clone(),
                    widget: d.widget.clone(),
                    diff: d.diff.clone(),
                    r#enum: d.enumeration.clone(),
                    default_value: d.default.clone(),
                    section: d.section.clone(),
                    order: d.order as i32,
                    tooltip: d.tooltip.clone(),
                    as_name: d.as_name,
                    validators: d.validators.clone(),
                }),
                from: a.from.clone(),
                values: a
                    .values
                    .iter()
                    .map(|v| pb::EnumValue {
                        value: v.value.clone(),
                        label: v.label.clone(),
                    })
                    .collect(),
            }
        })
        .collect()
}

/// A lifecycle (`domainpb.LifecyclesToPB`): what the protocol has a field for, never the bound algorithms.
pub fn lifecycle_info(l: &def::Lifecycle) -> pb::Lifecycle {
    let criteria = |cs: &[Value]| -> Vec<pb::Criterion> {
        cs.iter()
            .map(|c| {
                let o = obj(c);
                let s = |k: &str| o.get(k).and_then(scalar).unwrap_or_default();
                pb::Criterion {
                    name: s("name"),
                    expr: s("expr"),
                    description: s("description"),
                }
            })
            .collect()
    };
    pb::Lifecycle {
        name: l.name.clone(),
        description: l.description.clone(),
        initial: l.initial.clone(),
        states: l
            .states
            .iter()
            .map(|s| pb::LifecycleState {
                name: s.name.clone(),
                description: s.description.clone(),
                not_committable: s.not_committable,
                r#final: s.is_final,
            })
            .collect(),
        transitions: l
            .transitions
            .iter()
            .map(|t| {
                let req = t.requires.clone().unwrap_or_default();
                pb::LifecycleTransition {
                    name: t.name.clone(),
                    description: t.description.clone(),
                    from: t.from.clone(),
                    to: t.to.clone(),
                    permission: t.permission.clone(),
                    guard: t.guard.clone(),
                    requires_attributes: req.attributes,
                    requires_outgoing_links: req.outgoing_links,
                    children_states: t.children.as_ref().map(|c| c.states.clone()).unwrap_or_default(),
                    guards: t.guards.clone(),
                    actions: t.actions.clone(),
                    vetos: criteria(&t.vetos),
                    objectives: criteria(&t.objectives),
                }
            })
            .collect(),
    }
}

/// The definition of a domain a request carries, as `domainpb.DomainFromPB` reads it, in the JSON `json.Marshal`
/// writes of it (`Err`: its error). A form that does not read as one is left out, as `pbconv.FormFromPB` leaves it.
pub fn definition(p: &pb::Domain) -> Result<String, String> {
    use graph_catalog::decode;
    use serde_json::{Value as J, json};
    // a list Go builds by appending, or keeps when it holds something: null when empty
    fn list<T>(xs: &[T], f: impl Fn(&T) -> J) -> J {
        if xs.is_empty() {
            J::Null
        } else {
            J::Array(xs.iter().map(f).collect())
        }
    }
    let texts = |xs: &[String]| list(xs, |s| J::String(s.clone()));
    let attributes = |xs: &[pb::Attribute]| {
        list(xs, |a| {
            json!({"name": a.name, "label": a.label, "description": a.description, "type": a.r#type, "widget": a.widget,
                "diff": a.diff, "enum": a.r#enum, "default": a.default_value, "section": a.section, "order": a.order,
                "tooltip": a.tooltip, "asName": a.as_name, "validators": texts(&a.validators)})
        })
    };
    let form_type = graph_catalog::shape::Type {
        kind: "struct".into(),
        name: "form.Form".into(),
        go: "form.Form".into(),
        elem: None,
    };
    let form = |f: &Option<pbjson_types::Struct>| match f {
        Some(s) => {
            let v = J::Object(crate::convert::map(s));
            if decode::from_json(&form_type, &v).is_ok() {
                v
            } else {
                J::Null
            }
        }
        None => J::Null,
    };
    let criteria = |xs: &[pb::Criterion]| {
        list(
            xs,
            |c| json!({"name": c.name, "expr": c.expr, "description": c.description}),
        )
    };
    let d = json!({
        "name": p.name,
        "version": p.version,
        "description": p.description,
        "nodeTypes": list(&p.node_types, |n| json!({
            "name": n.name, "description": n.description, "attributes": attributes(&n.attributes),
            "extends": n.extends, "lifecycle": n.lifecycle,
            "document": n.document.as_ref().map(|d| json!({"contains": texts(&d.contains)})),
            "changeControlled": n.change_controlled, "validators": texts(&n.validators),
            "search": list(&n.search, |s| json!({"property": s.property, "text": s.text, "facet": s.facet})),
            "editor": n.editor, "icon": n.icon, "color": n.color,
            "structure": n.structure.as_ref().map(|t| json!({"kind": t.kind, "parent": t.parent, "root": t.root,
                "selfParent": t.self_parent})),
            "requires": list(&n.requires, |r| json!({"link": r.link, "count": r.count})),
            "adminOnly": n.admin_only, "additionalProperties": n.additional_properties, "form": form(&n.form),
        })),
        "linkTypes": list(&p.link_types, |l| json!({
            "name": l.name, "description": l.description, "from": l.from, "to": l.to, "compose": l.compose,
            "attributes": attributes(&l.attributes), "validators": texts(&l.validators), "label": l.label,
            "inverseLabel": l.inverse_label,
        })),
        "enums": list(&p.enums, |e| json!({"name": e.name, "description": e.description,
            "values": list(&e.values, |v| json!({"value": v.value, "label": v.label}))})),
        "lifecycles": list(&p.lifecycles, |l| json!({
            "name": l.name, "description": l.description, "initial": l.initial,
            "states": list(&l.states, |s| json!({"name": s.name, "description": s.description,
                "notCommittable": s.not_committable, "final": s.r#final})),
            "transitions": list(&l.transitions, |t| json!({
                "name": t.name, "description": t.description, "from": t.from, "to": t.to,
                "permission": t.permission, "guard": t.guard,
                "requires": {"attributes": texts(&t.requires_attributes),
                    "outgoingLinks": texts(&t.requires_outgoing_links)},
                "guards": texts(&t.guards), "actions": texts(&t.actions),
                "vetos": criteria(&t.vetos), "objectives": criteria(&t.objectives),
                "children": (!t.children_states.is_empty()).then(|| json!({"states": t.children_states})),
            })),
        })),
        "algorithms": list(&p.algorithms, |a| json!({
            "name": a.name, "description": a.description, "type": a.r#type, "language": a.language, "code": a.code,
            "params": list(&a.params, |x| json!({"name": x.name, "type": x.r#type, "description": x.description,
                "required": x.required, "values": texts(&x.values),
                "default": x.default_value.as_ref().map(crate::convert::json)})),
        })),
        "algorithmInstances": list(&p.algorithm_instances, |i| json!({
            "name": i.name, "description": i.description, "algorithm": i.algorithm,
            "values": i.values.as_ref().filter(|s| !s.fields.is_empty()).map(|s| J::Object(crate::convert::map(s))),
        })),
        "itemTypes": list(&p.item_types, |t| json!({"name": t.name, "description": t.description,
            "attributes": attributes(&t.attributes)})),
        "importers": list(&p.importers, |i| json!({"name": i.name, "label": i.label, "description": i.description,
            "params": attributes(&i.params)})),
    });
    let v = decode::from_json(&decode::domain_type(), &d)?;
    serde_json::to_string(&v).map_err(|e| e.to_string())
}
