//! The form, its definition checks and the validation of the values (`form.go`).
use std::{fmt, sync::LazyLock};

use regex::Regex;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    condition::{Condition, check_conditions},
    widgets::{
        ConstraintType, Env, ValueKind, WIDGET_DECISION, WIDGET_LINK, WIDGET_NEW_NODE, lookup,
        names,
    },
};

/// What a person sees and fills in to carry out a human action.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Form {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// The label of the button sending the values.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub submit: String,
    /// The type of the artifact recording the values when the action has no code (default: the action name).
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub artifact: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fields: Vec<Field>,
}

/// One input of a form, rendered by its widget.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Field {
    /// The key of the value.
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub widget: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub help: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub placeholder: String,
    /// Groups fields, in order of first appearance.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub section: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub required: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub default: Option<Value>,
    /// Limit the choices of the widget; their keys are the ones it declares.
    #[serde(default, skip_serializing_if = "Map::is_empty")]
    pub constraints: Map<String, Value>,
    /// The field is shown, checked and submitted only while every condition holds (none: always).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub visible_when: Vec<Condition>,
    /// The field must be filled in while every condition holds (`required`: always).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub required_when: Vec<Condition>,
    /// The sub-fields of a group: each entry of its value is an object of their values.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fields: Vec<Field>,
}

fn is_false(b: &bool) -> bool {
    !*b
}

impl Form {
    /// Reports whether the form declares nothing (Go's nil form is `None`: see [`form_is_zero`]).
    pub fn is_zero(&self) -> bool {
        self.fields.is_empty() && self.title.is_empty() && self.description.is_empty()
    }

    /// Returns the field of that name.
    pub fn field(&self, name: &str) -> Option<&Field> {
        self.fields.iter().find(|x| x.name == name)
    }
}

/// Reports whether a form, maybe none, declares nothing.
pub fn form_is_zero(f: Option<&Form>) -> bool {
    f.is_none_or(Form::is_zero)
}

/// A problem of a form definition (`path` relative to the form) or of a submitted value (`field`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Issue {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub field: String,
    #[serde(default)]
    pub message: String,
}

impl Issue {
    pub(crate) fn at(path: impl Into<String>, message: impl Into<String>) -> Issue {
        Issue {
            path: path.into(),
            message: message.into(),
            ..Issue::default()
        }
    }

    pub(crate) fn of(field: impl Into<String>, message: impl Into<String>) -> Issue {
        Issue {
            field: field.into(),
            message: message.into(),
            ..Issue::default()
        }
    }
}

impl fmt::Display for Issue {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if !self.field.is_empty() {
            return write!(f, "{}: {}", self.field, self.message);
        }
        if !self.path.is_empty() {
            return write!(f, "{}: {}", self.path, self.message);
        }
        f.write_str(&self.message)
    }
}

/// A list of issues, an error when not empty.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Issues(pub Vec<Issue>);

impl std::ops::Deref for Issues {
    type Target = Vec<Issue>;
    fn deref(&self) -> &Vec<Issue> {
        &self.0
    }
}

impl std::ops::DerefMut for Issues {
    fn deref_mut(&mut self) -> &mut Vec<Issue> {
        &mut self.0
    }
}

impl From<Vec<Issue>> for Issues {
    fn from(v: Vec<Issue>) -> Issues {
        Issues(v)
    }
}

impl IntoIterator for Issues {
    type Item = Issue;
    type IntoIter = std::vec::IntoIter<Issue>;
    fn into_iter(self) -> Self::IntoIter {
        self.0.into_iter()
    }
}

impl fmt::Display for Issues {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let parts: Vec<String> = self.0.iter().map(Issue::to_string).collect();
        f.write_str(&parts.join("; "))
    }
}

impl std::error::Error for Issues {}

/// Names, as the fixed source of a link field (`fromKey`), the node a form creates or edits: the form of a node type
/// (ADR 0115 §7).
pub const SELF_KEY: &str = "$self";

/// The shape of a field name.
pub static NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[A-Za-z][A-Za-z0-9_]*$").unwrap());

/// Reports the problems of a form definition: names, widgets known to the registry, constraints declared by the
/// widget and of their type, the constraints a widget requires, the references between fields, the conditions (on an
/// earlier field) and the sub-fields of a group (no group nor link field inside a group).
pub fn check_form(f: &Form) -> Issues {
    check_fields(&f.fields, "fields", false)
}

fn check_fields(fields: &[Field], path: &str, nested: bool) -> Issues {
    let mut out = Issues::default();
    let mut seen = std::collections::HashSet::new();
    let no_values = Map::new();
    for (j, fd) in fields.iter().enumerate() {
        let at = format!("{path}[{j}]");
        if !NAME_RE.is_match(&fd.name) {
            out.push(Issue::at(
                format!("{at}.name"),
                format!(
                    "invalid field name {} (a letter, then letters, digits or _)",
                    quote(&fd.name)
                ),
            ));
        } else if seen.contains(&fd.name) {
            out.push(Issue::at(
                format!("{at}.name"),
                format!("duplicate field {}", quote(&fd.name)),
            ));
        }
        seen.insert(fd.name.clone());
        out.extend(check_conditions(fields, j, "visibleWhen", &fd.visible_when, &at).0);
        out.extend(check_conditions(fields, j, "requiredWhen", &fd.required_when, &at).0);
        let Some(w) = lookup(&fd.widget) else {
            out.push(Issue::at(
                format!("{at}.widget"),
                format!(
                    "unknown widget {} (known: {})",
                    quote(&fd.widget),
                    names().join(", ")
                ),
            ));
            continue;
        };
        if nested
            && (w.value == ValueKind::Objects
                || w.value == ValueKind::Links
                || w.name == WIDGET_NEW_NODE
                || w.name == WIDGET_DECISION)
        {
            out.push(Issue::at(
                format!("{at}.widget"),
                format!("a group holds no {} field", w.name),
            ));
        }
        if w.value == ValueKind::Objects {
            if fd.fields.is_empty() {
                out.push(Issue::at(
                    format!("{at}.fields"),
                    "a group needs at least one sub-field",
                ));
            }
            out.extend(check_fields(&fd.fields, &format!("{at}.fields"), true).0);
        } else if !fd.fields.is_empty() {
            out.push(Issue::at(
                format!("{at}.fields"),
                format!("only a group has sub-fields, not a {}", w.name),
            ));
        }
        for k in sorted_keys(&fd.constraints) {
            let p = format!("{at}.constraints.{k}");
            let Some(c) = w.constraint(&k) else {
                out.push(Issue::at(
                    p,
                    format!("widget {} has no constraint {}", w.name, quote(&k)),
                ));
                continue;
            };
            let msg = c.r#type.check(&fd.constraints[&k]);
            if !msg.is_empty() {
                out.push(Issue::at(p, msg));
                continue;
            }
            if c.r#type == ConstraintType::Field {
                let reference = fd.constraints[&k].as_str().unwrap_or_default();
                match field_of(fields, reference) {
                    Some(other) if reference != fd.name => {
                        if !c.widgets.is_empty() && !c.widgets.contains(&other.widget) {
                            out.push(Issue::at(
                                p,
                                format!(
                                    "field {} is a {}, not a {}",
                                    quote(reference),
                                    other.widget,
                                    c.widgets.join(" or ")
                                ),
                            ));
                        }
                    }
                    _ => out.push(Issue::at(p, format!("no other field {}", quote(reference)))),
                }
            }
        }
        for c in &w.constraints {
            if c.required && !fd.constraints.contains_key(&c.name) {
                out.push(Issue::at(
                    format!("{at}.constraints"),
                    format!("widget {} needs the constraint {}", w.name, quote(&c.name)),
                ));
            }
        }
        if let Some(define) = &w.define {
            for msg in define(fd) {
                out.push(Issue::at(at.clone(), msg));
            }
        }
        if let Some(d) = &fd.default
            && !matches!(
                w.value,
                ValueKind::None | ValueKind::Links | ValueKind::Objects | ValueKind::Object
            )
        {
            let is = w.check(fd, d, &Env::default(), &no_values);
            if let Some(first) = is.into_iter().next() {
                out.push(Issue::at(format!("{at}.default"), first));
            }
        }
    }
    out
}

pub(crate) fn field_of<'a>(fields: &'a [Field], name: &str) -> Option<&'a Field> {
    fields.iter().find(|x| x.name == name)
}

/// Checks the values a person submitted against the form and returns them cleaned (lists normalized, the defaults
/// of the fields left out, the links of a link field completed with their source, the entries of a group cleaned the
/// same way): a value for a field the form does not declare, a missing required value, a value of the wrong kind or
/// one its constraints exclude is an issue. A field its conditions hide is left out, whatever was sent for it. env
/// resolves the node keys (no `node`: unchecked).
pub fn validate(f: &Form, values: &Map<String, Value>, env: &Env) -> (Map<String, Value>, Issues) {
    validate_fields(f, &f.fields, values, env)
}

fn validate_fields(
    f: &Form,
    fields: &[Field],
    values: &Map<String, Value>,
    env: &Env,
) -> (Map<String, Value>, Issues) {
    let mut out = Issues::default();
    let mut clean = Map::new();
    let mut env = env.clone();
    for k in sorted_keys(values) {
        if field_of(fields, &k).is_none() {
            out.push(Issue::of(k, "not a field of the form"));
        }
    }
    for fd in fields {
        let Some(w) = lookup(&fd.widget) else {
            out.push(Issue::of(
                fd.name.clone(),
                format!("unknown widget {}", quote(&fd.widget)),
            ));
            continue;
        };
        if w.value == ValueKind::None || !fd.visible(&clean) {
            continue;
        }
        let mut v = match values.get(&fd.name) {
            Some(v) if !empty(v) => v.clone(),
            _ => fd.default.clone().unwrap_or(Value::Null),
        };
        if empty(&v) {
            if fd.required_for(&clean) {
                out.push(Issue::of(fd.name.clone(), "required"));
            }
            continue;
        }
        v = normalize(&v);
        match w.value {
            ValueKind::Links => v = complete_links(f, fd, v, values),
            ValueKind::Objects => {
                let (c, is) = validate_entries(f, fd, v, &env);
                v = c;
                out.extend(is.0);
            }
            _ => {}
        }
        for msg in w.check(fd, &v, &env, values) {
            out.push(Issue::of(fd.name.clone(), msg));
        }
        if fd.widget == WIDGET_NEW_NODE {
            // the links of the fields after it may start from or go to the nodes it creates
            env = env.with(new_nodes_of(fd, &v));
        }
        clean.insert(fd.name.clone(), v);
    }
    (clean, out)
}

/// Checks and cleans the entries of a group, each against its sub-fields.
fn validate_entries(f: &Form, fd: &Field, v: Value, env: &Env) -> (Value, Issues) {
    let Value::Array(list) = v else {
        return (v, Issues::default());
    };
    let mut out = Issues::default();
    let mut entries = Vec::with_capacity(list.len());
    for (i, e) in list.into_iter().enumerate() {
        let Value::Object(m) = &e else {
            entries.push(e);
            continue;
        };
        let (c, is) = validate_fields(f, &fd.fields, m, env);
        for x in is.0 {
            out.push(Issue::of(
                fd.name.clone(),
                format!("entry {}, {}: {}", i + 1, x.field, x.message),
            ));
        }
        entries.push(Value::Object(c));
    }
    (Value::Array(entries), out)
}

/// Reports a value the person left blank.
pub(crate) fn empty(v: &Value) -> bool {
    match v {
        Value::Null => true,
        Value::String(s) => s.trim().is_empty(),
        Value::Array(a) => a.is_empty(),
        Value::Object(m) => m.is_empty(),
        _ => false,
    }
}

/// Gives a value the shapes of decoded JSON as Go's `encoding/json` writes it back: a whole number written as a float
/// (`2.0`) becomes the integer it is, the entries of a list are normalized too.
pub(crate) fn normalize(v: &Value) -> Value {
    match v {
        Value::Number(n) if !n.is_i64() && !n.is_u64() => match n.as_f64() {
            Some(x) if x.fract() == 0.0 && x.abs() < 9.0e15 => Value::from(x as i64),
            _ => v.clone(),
        },
        Value::Array(a) => Value::Array(a.iter().map(normalize).collect()),
        _ => v.clone(),
    }
}

/// Go's `reflect.DeepEqual` over decoded JSON: numbers are equal when their values are.
pub(crate) fn values_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => x.as_f64() == y.as_f64(),
        (Value::Array(x), Value::Array(y)) => {
            x.len() == y.len() && x.iter().zip(y).all(|(a, b)| values_equal(a, b))
        }
        (Value::Object(x), Value::Object(y)) => {
            x.len() == y.len()
                && x.iter()
                    .all(|(k, a)| y.get(k).is_some_and(|b| values_equal(a, b)))
        }
        _ => a == b,
    }
}

/// Reads the node keys of the value of a node or new node field: a key, keys, a new node {key}, new nodes.
pub(crate) fn keys_of(v: &Value) -> Vec<String> {
    match v {
        Value::String(x) if !x.is_empty() => vec![x.clone()],
        Value::Object(m) => match m.get("key").and_then(Value::as_str) {
            Some(k) if !k.is_empty() => vec![k.to_string()],
            _ => Vec::new(),
        },
        Value::Array(a) => a.iter().flat_map(keys_of).collect(),
        _ => Vec::new(),
    }
}

/// A node a new node field asks the change to create.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct NewNode {
    pub field: String,
    pub key: String,
    #[serde(rename = "type")]
    pub r#type: String,
    #[serde(default, skip_serializing_if = "Map::is_empty")]
    pub props: Map<String, Value>,
}

/// Lists the nodes the new node fields of validated values create, in the order of the fields.
pub fn new_nodes(f: &Form, values: &Map<String, Value>) -> Vec<NewNode> {
    f.fields
        .iter()
        .filter(|fd| fd.widget == WIDGET_NEW_NODE)
        .flat_map(|fd| new_nodes_of(fd, values.get(&fd.name).unwrap_or(&Value::Null)))
        .collect()
}

pub(crate) fn new_nodes_of(fd: &Field, v: &Value) -> Vec<NewNode> {
    let mut out = Vec::new();
    for m in objects_of(v) {
        let key = str_of(m, "key");
        let typ = str_of(m, "type");
        let props = m
            .get("props")
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        if !key.is_empty() && !typ.is_empty() {
            out.push(NewNode {
                field: fd.name.clone(),
                key: key.to_string(),
                r#type: typ.to_string(),
                props,
            });
        }
    }
    out
}

/// The ruling of a decision point a decision field asks for.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Ruling {
    pub field: String,
    pub point: String,
    pub option: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub justification: String,
}

/// Lists the rulings of the decision fields of validated values, in the order of the fields.
pub fn rulings(f: &Form, values: &Map<String, Value>) -> Vec<Ruling> {
    let mut out = Vec::new();
    for fd in &f.fields {
        if fd.widget != WIDGET_DECISION {
            continue;
        }
        for m in objects_of(values.get(&fd.name).unwrap_or(&Value::Null)) {
            let (p, o, j) = (
                str_of(m, "point"),
                str_of(m, "option"),
                str_of(m, "justification"),
            );
            if !p.is_empty() && !o.is_empty() {
                out.push(Ruling {
                    field: fd.name.clone(),
                    point: p.to_string(),
                    option: o.to_string(),
                    justification: j.to_string(),
                });
            }
        }
    }
    out
}

/// Reads an object, or a list of objects.
pub(crate) fn objects_of(v: &Value) -> Vec<&Map<String, Value>> {
    match v {
        Value::Object(m) => vec![m],
        Value::Array(a) => a.iter().filter_map(Value::as_object).collect(),
        _ => Vec::new(),
    }
}

/// The string under a key of an object ("" when it is none).
pub(crate) fn str_of<'a>(m: &'a Map<String, Value>, k: &str) -> &'a str {
    m.get(k).and_then(Value::as_str).unwrap_or_default()
}

/// A link a link field asks for.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Link {
    pub field: String,
    pub from: String,
    #[serde(rename = "type")]
    pub r#type: String,
    pub to: String,
}

/// Lists the links of the link fields of validated values, in the order of the fields.
pub fn links(f: &Form, values: &Map<String, Value>) -> Vec<Link> {
    let mut out = Vec::new();
    for fd in &f.fields {
        if fd.widget != WIDGET_LINK {
            continue;
        }
        let Some(list) = values.get(&fd.name).and_then(Value::as_array) else {
            continue;
        };
        for m in list.iter().filter_map(Value::as_object) {
            let (from, typ, to) = (str_of(m, "from"), str_of(m, "type"), str_of(m, "to"));
            if !from.is_empty() && !typ.is_empty() && !to.is_empty() {
                out.push(Link {
                    field: fd.name.clone(),
                    from: from.to_string(),
                    r#type: typ.to_string(),
                    to: to.to_string(),
                });
            }
        }
    }
    out
}

/// Gives the links of a link field their source and their type when the field fixes them.
fn complete_links(f: &Form, fd: &Field, v: Value, values: &Map<String, Value>) -> Value {
    let Value::Array(list) = v else {
        return v;
    };
    let from = link_source(f, fd, values);
    let typ = match strings_of(fd.constraints.get("linkTypes")) {
        Some(lt) if lt.len() == 1 => lt[0].clone(),
        _ => String::new(),
    };
    Value::Array(
        list.into_iter()
            .map(|e| match e {
                Value::Object(mut c) => {
                    if str_of(&c, "from").is_empty() && !from.is_empty() {
                        c.insert("from".into(), Value::String(from.clone()));
                    }
                    if str_of(&c, "type").is_empty() && !typ.is_empty() {
                        c.insert("type".into(), Value::String(typ.clone()));
                    }
                    Value::Object(c)
                }
                e => e,
            })
            .collect(),
    )
}

/// The source a link field fixes: its fixed key, else the single node of its from field.
fn link_source(f: &Form, fd: &Field, values: &Map<String, Value>) -> String {
    let k = constraint_str(fd, "fromKey");
    if !k.is_empty() {
        return k.to_string();
    }
    let reference = constraint_str(fd, "from");
    if reference.is_empty() {
        return String::new();
    }
    let keys = keys_of(values.get(reference).unwrap_or(&Value::Null));
    match keys.len() {
        1 => return keys[0].clone(),
        0 => {}
        _ => return String::new(),
    }
    if let Some(Value::String(s)) = f.field(reference).and_then(|r| r.default.as_ref()) {
        return s.clone();
    }
    String::new()
}

/// The string value of a constraint ("" when it is none).
pub(crate) fn constraint_str<'a>(fd: &'a Field, name: &str) -> &'a str {
    str_of(&fd.constraints, name)
}

pub(crate) fn sorted_keys(m: &Map<String, Value>) -> Vec<String> {
    let mut out: Vec<String> = m.keys().cloned().collect();
    out.sort();
    out
}

/// Reads a list of strings of a constraint (`None` when it is not one).
pub(crate) fn strings_of(v: Option<&Value>) -> Option<Vec<String>> {
    match v? {
        Value::Array(a) => a
            .iter()
            .map(|e| e.as_str().map(str::to_string))
            .collect::<Option<Vec<_>>>(),
        _ => None,
    }
}

/// Go's `%q` of a string (`strconv.Quote`).
pub(crate) fn quote(s: &str) -> String {
    let mut b = String::with_capacity(s.len() + 2);
    b.push('"');
    for c in s.chars() {
        match c {
            '"' => b.push_str("\\\""),
            '\\' => b.push_str("\\\\"),
            '\u{7}' => b.push_str("\\a"),
            '\u{8}' => b.push_str("\\b"),
            '\u{c}' => b.push_str("\\f"),
            '\n' => b.push_str("\\n"),
            '\r' => b.push_str("\\r"),
            '\t' => b.push_str("\\t"),
            '\u{b}' => b.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => b.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() => b.push_str(&format!("\\u{:04x}", c as u32)),
            c => b.push(c),
        }
    }
    b.push('"');
    b
}

/// Go's `%g` of a float64 (shortest representation, an exponent from 1e+06 on or under 1e-04).
pub(crate) fn fmt_g(x: f64) -> String {
    if x.is_nan() {
        return "NaN".into();
    }
    if x.is_infinite() {
        return if x > 0.0 { "+Inf" } else { "-Inf" }.into();
    }
    if x == 0.0 {
        return if x.is_sign_negative() { "-0" } else { "0" }.into();
    }
    let e = format!("{x:e}");
    let (mant, exp) = e.split_once('e').unwrap_or((&e, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    if !(-4..6).contains(&exp) {
        let sign = if exp < 0 { '-' } else { '+' };
        return format!("{mant}e{sign}{:02}", exp.abs());
    }
    format!("{x}")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        Condition, Decision, Node, WIDGET_DECISION, WIDGET_FILE, WIDGET_GROUP, WIDGET_LINK,
        WIDGET_MARKDOWN, WIDGET_MULTISELECT, WIDGET_NEW_NODE, WIDGET_NODE, WIDGET_NUMBER,
        WIDGET_SELECT, WIDGET_TEXT, WIDGET_TEXTAREA, Widget, register,
    };
    use serde_json::json;
    use std::{collections::HashMap, sync::Arc};

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => panic!("not an object"),
        }
    }

    fn field(name: &str, widget: &str) -> Field {
        Field {
            name: name.into(),
            widget: widget.into(),
            ..Field::default()
        }
    }

    fn with(mut f: Field, constraints: Value) -> Field {
        f.constraints = obj(constraints);
        f
    }

    fn link_form() -> Form {
        Form {
            fields: vec![
                Field {
                    required: true,
                    ..with(
                        field("requirement", WIDGET_NODE),
                        json!({"types": ["alm@Requirement"]}),
                    )
                },
                with(
                    field("realizedBy", WIDGET_LINK),
                    json!({"from": "requirement", "linkTypes": ["alm@realized_by"], "toTypes": ["alm@Application"], "max": 2}),
                ),
                Field {
                    required: true,
                    ..with(field("why", WIDGET_TEXTAREA), json!({"minLength": 3}))
                },
                Field {
                    default: Some(json!("low")),
                    ..with(
                        field("level", WIDGET_SELECT),
                        json!({"options": ["low", "high"]}),
                    )
                },
                with(
                    field("intro", WIDGET_MARKDOWN),
                    json!({"text": "Pick a requirement."}),
                ),
            ],
            ..Form::default()
        }
    }

    fn env() -> Env {
        let nodes: HashMap<&str, Node> = [
            ("REQ-1", "alm@Requirement", "approved"),
            ("APP-1", "alm@WebApplication", ""),
            ("APP-2", "alm@Application", ""),
            ("CMP-1", "alm@Component", ""),
        ]
        .into_iter()
        .map(|(k, t, s)| {
            (
                k,
                Node {
                    key: k.into(),
                    r#type: t.into(),
                    state: s.into(),
                    ..Node::default()
                },
            )
        })
        .collect();
        Env {
            node: Some(Arc::new(move |k: &str| nodes.get(k).cloned())),
            is_a: Some(Arc::new(|t: &str, s: &str| {
                t == s || t == "alm@WebApplication" && s == "alm@Application"
            })),
            decision: None,
        }
    }

    fn assert_contains(got: &str, wants: &[&str]) {
        for want in wants {
            assert!(got.contains(want), "missing {want:?} in {got}");
        }
    }

    #[test]
    fn check_form_reports() {
        let is = check_form(&link_form());
        assert!(is.is_empty(), "valid form: {is}");
        let bad = Form {
            fields: vec![
                field("1x", WIDGET_TEXT),
                field("a", "slider"),
                field("a", WIDGET_SELECT),
                with(
                    field("b", WIDGET_TEXT),
                    json!({"options": ["x"], "pattern": "("}),
                ),
                with(field("c", WIDGET_LINK), json!({"from": "b"})),
                field("d", WIDGET_LINK),
                with(field("e", WIDGET_NUMBER), json!({"min": "zero"})),
                Field {
                    default: Some(json!("z")),
                    ..with(field("f", WIDGET_SELECT), json!({"options": ["x"]}))
                },
            ],
            ..Form::default()
        };
        assert_contains(
            &check_form(&bad).to_string(),
            &[
                r#"fields[0].name: invalid field name "1x""#,
                r#"fields[1].widget: unknown widget "slider""#,
                r#"fields[2].name: duplicate field "a""#,
                r#"fields[2].constraints: widget select needs the constraint "options""#,
                r#"fields[3].constraints.options: widget text has no constraint "options""#,
                "fields[3]: invalid pattern",
                r#"fields[4].constraints.from: field "b" is a text, not a node"#,
                "fields[5]: a link field names its source",
                "fields[6].constraints.min: expects a number",
                r#"fields[7].default: "z" is not one of the options"#,
            ],
        );
    }

    #[test]
    fn validate_values() {
        let f = link_form();
        let (clean, is) = validate(
            &f,
            &obj(json!({
                "requirement": "REQ-1",
                "realizedBy": [{"to": "APP-1"}, {"to": "APP-2", "type": "alm@realized_by"}],
                "why": "because",
            })),
            &env(),
        );
        assert!(is.is_empty(), "issues: {is}");
        assert_eq!(clean["level"], json!("low"), "default not applied");
        let l = links(&f, &clean);
        assert_eq!(l.len(), 2, "links: {l:?}");
        assert_eq!(
            l[0],
            Link {
                field: "realizedBy".into(),
                from: "REQ-1".into(),
                r#type: "alm@realized_by".into(),
                to: "APP-1".into()
            }
        );

        let (_, is) = validate(
            &f,
            &obj(json!({
                "requirement": "APP-2",
                "realizedBy": [{"to": "CMP-1"}, {"to": "NOPE"}, {"to": "APP-2"}],
                "why": "x",
                "level": "medium",
                "extra": 1,
            })),
            &env(),
        );
        assert_contains(
            &is.to_string(),
            &[
                "extra: not a field of the form",
                "requirement: APP-2 is a alm@Application, not one of alm@Requirement",
                "realizedBy: link 1: CMP-1 is a alm@Component, not one of alm@Application",
                r#"realizedBy: link 2: unknown node "NOPE""#,
                "realizedBy: at most 2 links",
                "why: at least 3 characters",
                r#"level: "medium" is not one of the options"#,
            ],
        );

        let (_, is) = validate(&f, &Map::new(), &env());
        assert_contains(&is.to_string(), &["requirement: required", "why: required"]);
    }

    #[test]
    fn validate_node_constraints() {
        let f = Form {
            fields: vec![with(
                field("n", WIDGET_NODE),
                json!({"multiple": true, "max": 1, "states": ["approved"], "inChange": true}),
            )],
            ..Form::default()
        };
        let (_, is) = validate(&f, &obj(json!({"n": ["REQ-1", "APP-2"]})), &env());
        assert_contains(
            &is.to_string(),
            &[
                "REQ-1 is not a node of the change",
                "APP-2 is in no state, not one of approved",
                "at most 1 nodes",
            ],
        );
    }

    #[test]
    #[should_panic(expected = "registered twice")]
    fn register_duplicate_panics() {
        register(Widget::new(WIDGET_TEXT));
    }

    #[test]
    fn conditional_fields() {
        let cond = |field: &str| Condition {
            field: field.into(),
            ..Condition::default()
        };
        let f = Form {
            fields: vec![
                Field {
                    required: true,
                    ..with(
                        field("decision", WIDGET_SELECT),
                        json!({"options": ["accept", "reject"]}),
                    )
                },
                Field {
                    visible_when: vec![Condition {
                        equals: Some(json!("reject")),
                        ..cond("decision")
                    }],
                    required: true,
                    ..field("reason", WIDGET_TEXTAREA)
                },
                with(
                    field("tags", WIDGET_MULTISELECT),
                    json!({"options": ["a", "b"]}),
                ),
                Field {
                    required_when: vec![Condition {
                        r#in: Some(vec![json!("b")]),
                        ..cond("tags")
                    }],
                    ..field("note", WIDGET_TEXT)
                },
                Field {
                    visible_when: vec![Condition {
                        set: Some(true),
                        ..cond("reason")
                    }],
                    ..field("extra", WIDGET_TEXT)
                },
            ],
            ..Form::default()
        };
        let is = check_form(&f);
        assert!(is.is_empty(), "valid form: {is}");
        let (clean, is) = validate(
            &f,
            &obj(json!({"decision": "accept", "reason": "ignored", "extra": "gone"})),
            &Env::default(),
        );
        assert!(
            is.is_empty() && !clean.contains_key("reason") && !clean.contains_key("extra"),
            "hidden fields: {clean:?} {is}"
        );
        let (_, is) = validate(
            &f,
            &obj(json!({"decision": "reject", "tags": ["a", "b"]})),
            &Env::default(),
        );
        assert_contains(&is.to_string(), &["reason: required", "note: required"]);
        let bad = Form {
            fields: vec![
                Field {
                    visible_when: vec![Condition {
                        equals: Some(json!("x")),
                        ..cond("b")
                    }],
                    ..field("a", WIDGET_TEXT)
                },
                Field {
                    required_when: vec![cond("a")],
                    ..field("b", WIDGET_TEXT)
                },
                with(field("c", WIDGET_MARKDOWN), json!({"text": "t"})),
                Field {
                    visible_when: vec![Condition {
                        set: Some(true),
                        ..cond("c")
                    }],
                    ..field("d", WIDGET_TEXT)
                },
            ],
            ..Form::default()
        };
        assert_contains(
            &check_form(&bad).to_string(),
            &[
                r#"fields[0].visibleWhen[0].field: no field "b" before this one"#,
                "fields[1].requiredWhen[0]: a condition tests exactly one of equals, in or set",
                r#"fields[3].visibleWhen[0].field: field "c" is a markdown"#,
            ],
        );
    }

    #[test]
    fn group_field() {
        let f = Form {
            fields: vec![Field {
                fields: vec![
                    Field {
                        required: true,
                        ..field("title", WIDGET_TEXT)
                    },
                    Field {
                        default: Some(json!("low")),
                        ..with(
                            field("level", WIDGET_SELECT),
                            json!({"options": ["low", "high"]}),
                        )
                    },
                    Field {
                        required_when: vec![Condition {
                            field: "level".into(),
                            equals: Some(json!("high")),
                            ..Condition::default()
                        }],
                        ..field("mitigation", WIDGET_TEXT)
                    },
                ],
                ..with(field("risks", WIDGET_GROUP), json!({"max": 2}))
            }],
            ..Form::default()
        };
        let is = check_form(&f);
        assert!(is.is_empty(), "valid form: {is}");
        let (clean, is) = validate(
            &f,
            &obj(json!({"risks": [{"title": "outage"}]})),
            &Env::default(),
        );
        assert!(is.is_empty(), "{is}");
        assert_eq!(
            clean["risks"][0]["level"],
            json!("low"),
            "default of a sub-field"
        );
        let (_, is) = validate(
            &f,
            &obj(json!({"risks": [
                {"title": "a", "level": "high"}, {"level": "low", "x": 1}, {"title": "c"},
            ]})),
            &Env::default(),
        );
        assert_contains(
            &is.to_string(),
            &[
                "entry 1, mitigation: required",
                "entry 2, title: required",
                "entry 2, x: not a field",
                "at most 2 entries",
            ],
        );
        let bad = Form {
            fields: vec![
                field("g", WIDGET_GROUP),
                Field {
                    fields: vec![with(field("l", WIDGET_LINK), json!({"fromKey": "X"}))],
                    ..field("h", WIDGET_GROUP)
                },
                Field {
                    fields: vec![field("x", WIDGET_TEXT)],
                    ..field("t", WIDGET_TEXT)
                },
            ],
            ..Form::default()
        };
        assert_contains(
            &check_form(&bad).to_string(),
            &[
                "fields[0].fields: a group needs at least one sub-field",
                "fields[1].fields[0].widget: a group holds no link field",
                "fields[2].fields: only a group has sub-fields",
            ],
        );
    }

    #[test]
    fn new_node_decision_file() {
        let f = Form {
            fields: vec![
                Field {
                    required: true,
                    ..with(
                        field("req", WIDGET_NEW_NODE),
                        json!({"types": ["alm@Requirement"], "properties": ["title"]}),
                    )
                },
                with(
                    field("realizedBy", WIDGET_LINK),
                    json!({"from": "req", "linkTypes": ["alm@realized_by"]}),
                ),
                with(field("choice", WIDGET_DECISION), json!({"justify": true})),
                with(
                    field("spec", WIDGET_FILE),
                    json!({"accept": [".csv", "text/*"], "maxBytes": 8}),
                ),
            ],
            ..Form::default()
        };
        let is = check_form(&f);
        assert!(is.is_empty(), "valid form: {is}");
        let mut e = env();
        e.decision = Some(Arc::new(|id: &str| match id {
            "D1" => Some(Decision {
                id: "D1".into(),
                question: "which?".into(),
                options: vec!["a".into(), "b".into()],
                decided: false,
            }),
            "D2" => Some(Decision {
                id: "D2".into(),
                question: "done".into(),
                options: vec![],
                decided: true,
            }),
            _ => None,
        }));
        let good = obj(json!({
            "req": {"key": "REQ-9", "type": "alm@Requirement", "props": {"title": "New"}},
            "realizedBy": [{"type": "alm@realized_by", "to": "APP-1"}],
            "choice": {"point": "D1", "option": "a", "justification": "cheaper"},
            "spec": {"name": "x.csv", "type": "text/csv", "content": "YSxiCg=="},
        }));
        let (clean, is) = validate(&f, &good, &e);
        assert!(is.is_empty(), "{is}");
        let nn = new_nodes(&f, &clean);
        assert!(
            nn.len() == 1 && nn[0].key == "REQ-9" && nn[0].props["title"] == json!("New"),
            "new nodes: {nn:?}"
        );
        let l = links(&f, &clean);
        assert!(
            l.len() == 1 && l[0].from == "REQ-9",
            "a link from the new node: {l:?}"
        );
        let r = rulings(&f, &clean);
        assert!(r.len() == 1 && r[0].option == "a", "rulings: {r:?}");
        let bad = obj(json!({
            "req": {"key": "REQ-1", "type": "alm@Component", "props": {"owner": "x"}},
            "choice": {"point": "D2", "option": "a"},
            "spec": {"name": "x.pdf", "type": "application/pdf", "content": "YWJjZGVmZ2hpams="},
        }));
        let (_, is) = validate(&f, &bad, &e);
        assert_contains(
            &is.to_string(),
            &[
                "req: alm@Component is not one of alm@Requirement",
                "req: REQ-1 already exists",
                "req: owner is not a property asked",
                "choice: needs a justification",
                "spec: x.pdf is larger than 8 bytes",
            ],
        );
        let (_, is) = validate(
            &f,
            &obj(json!({
                "req": good["req"],
                "choice": {"point": "D2", "option": "a", "justification": "j"},
                "spec": {"name": "x.pdf", "type": "application/pdf", "content": "YQ=="},
            })),
            &e,
        );
        assert_contains(
            &is.to_string(),
            &[
                r#"choice: the decision point "done" is already decided"#,
                "spec: x.pdf is not one of .csv, text/*",
            ],
        );
    }

    #[test]
    fn json_shapes() {
        let f: Form = serde_json::from_value(json!({
            "fields": [{"name": "a", "widget": "text", "visibleWhen": [{"field": "b", "in": []}], "default": null}]
        }))
        .unwrap();
        assert_eq!(
            serde_json::to_value(&f).unwrap(),
            json!({"fields": [{"name": "a", "widget": "text", "visibleWhen": [{"field": "b"}]}]})
        );
        assert_eq!(fmt_g(3.0), "3");
        assert_eq!(fmt_g(2.5), "2.5");
        assert_eq!(fmt_g(524288.0), "524288");
        assert_eq!(fmt_g(1e6), "1e+06");
        assert_eq!(fmt_g(1.5e7), "1.5e+07");
        assert_eq!(fmt_g(0.0001), "0.0001");
        assert_eq!(fmt_g(0.00001), "1e-05");
        assert_eq!(normalize(&json!([2.0, 2.5])), json!([2, 2.5]));
    }
}
