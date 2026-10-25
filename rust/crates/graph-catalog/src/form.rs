//! The check of a form's definition (`contracts/form`, `CheckForm`, ADR 0115): field names, the widgets of the
//! registry (read from the catalogue Go writes for the web, `widgetCatalog.gen.ts`), their constraints, the
//! conditions and the defaults. What only Go words (a pattern Go's regexp refuses, a default whose check reads a
//! date, a bound printed in an exponent) is [`Unsure`].
use std::collections::{BTreeMap, HashSet};
use std::sync::LazyLock;

use serde::Deserialize;
use serde_json::{Map, Value};

use crate::decode::go_quote;

/// A check whose words only Go has: the caller leaves the request to it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Unsure(pub &'static str);

/// An issue of a form: the path below the form, its message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Issue {
    pub path: String,
    pub message: String,
}

/// A widget of the registry (`form.Widget`), as the catalogue lists it.
#[derive(Debug, Clone, Deserialize)]
pub struct Widget {
    pub name: String,
    pub value: String,
    #[serde(default)]
    pub constraints: Vec<Constraint>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Constraint {
    pub name: String,
    #[serde(rename = "type")]
    pub typ: String,
    #[serde(default)]
    pub required: bool,
    #[serde(default)]
    pub widgets: Vec<String>,
}

/// The widgets registered in Go, sorted by name (`form.All`).
pub static WIDGETS: LazyLock<Vec<Widget>> = LazyLock::new(|| {
    let src = include_str!("../../../../web/src/lib/forms/widgetCatalog.gen.ts");
    let json = src
        .split_once("WIDGET_CATALOG: WidgetSpec[] = ")
        .and_then(|(_, rest)| rest.trim_end().strip_suffix(';'))
        .expect("the widget catalogue Go writes");
    serde_json::from_str(json).expect("the widget catalogue Go writes")
});

fn lookup(name: &str) -> Option<&'static Widget> {
    WIDGETS.iter().find(|w| w.name == name)
}

impl Widget {
    fn constraint(&self, name: &str) -> Option<&Constraint> {
        self.constraints.iter().find(|c| c.name == name)
    }
}

/// The widgets a creation form treats apart.
pub const MARKDOWN_WIDGET: &str = "markdown";
pub const LINK_WIDGET: &str = "link";
pub const NEW_NODE_WIDGET: &str = "newNode";
pub const DECISION_WIDGET: &str = "decision";
/// The key a link field of a creation form starts from: the node created.
pub const SELF_KEY: &str = "$self";

/// A field of a form, read from the JSON of its definition.
#[derive(Debug, Clone, Default)]
pub struct Field {
    pub name: String,
    pub widget: String,
    pub default: Option<Value>,
    pub constraints: BTreeMap<String, Value>,
    pub visible_when: Vec<Condition>,
    pub required_when: Vec<Condition>,
    pub fields: Vec<Field>,
}

#[derive(Debug, Clone, Default)]
pub struct Condition {
    pub field: String,
    pub equals: bool,
    /// Whether `in` names a list; a YAML `in: []` is a list the JSON no longer holds.
    pub in_list: bool,
    pub set: bool,
}

/// The fields of a form, from its JSON.
pub fn fields(form: &Value) -> Vec<Field> {
    list(form.get("fields")).iter().map(field).collect()
}

fn list(v: Option<&Value>) -> &[Value] {
    v.and_then(Value::as_array).map(Vec::as_slice).unwrap_or_default()
}

fn text(o: &Map<String, Value>, k: &str) -> String {
    o.get(k).and_then(Value::as_str).unwrap_or_default().to_string()
}

fn field(v: &Value) -> Field {
    let empty = Map::new();
    let o = v.as_object().unwrap_or(&empty);
    let conditions = |k: &str| {
        list(o.get(k))
            .iter()
            .map(|c| Condition {
                field: c.get("field").and_then(Value::as_str).unwrap_or_default().to_string(),
                equals: c.get("equals").is_some_and(|x| !x.is_null()),
                in_list: c.get("in").is_some_and(|x| !x.is_null()),
                set: c.get("set").is_some_and(|x| !x.is_null()),
            })
            .collect()
    };
    Field {
        name: text(o, "name"),
        widget: text(o, "widget"),
        default: o.get("default").filter(|x| !x.is_null()).cloned(),
        constraints: o
            .get("constraints")
            .and_then(Value::as_object)
            .map(|m| m.iter().map(|(k, x)| (k.clone(), x.clone())).collect())
            .unwrap_or_default(),
        visible_when: conditions("visibleWhen"),
        required_when: conditions("requiredWhen"),
        fields: list(o.get("fields")).iter().map(field).collect(),
    }
}

/// Checks a form (`form.CheckForm`). `exact`: the form was read from JSON (a stored version), not from YAML, whose
/// empty lists the JSON drops.
pub fn check(fields: &[Field], exact: bool) -> Result<Vec<Issue>, Unsure> {
    let mut out = Vec::new();
    check_fields(fields, "fields", false, exact, &mut out)?;
    Ok(out)
}

fn field_name_ok(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_alphabetic()) && c.all(|x| x.is_ascii_alphanumeric() || x == '_')
}

fn issue(out: &mut Vec<Issue>, path: impl Into<String>, message: impl Into<String>) {
    out.push(Issue {
        path: path.into(),
        message: message.into(),
    });
}

fn check_fields(fields: &[Field], path: &str, nested: bool, exact: bool, out: &mut Vec<Issue>) -> Result<(), Unsure> {
    let mut seen = HashSet::new();
    for (j, fd) in fields.iter().enumerate() {
        let at = format!("{path}[{j}]");
        if !field_name_ok(&fd.name) {
            issue(
                out,
                format!("{at}.name"),
                format!(
                    "invalid field name {} (a letter, then letters, digits or _)",
                    go_quote(&fd.name)
                ),
            );
        } else if seen.contains(&fd.name) {
            issue(
                out,
                format!("{at}.name"),
                format!("duplicate field {}", go_quote(&fd.name)),
            );
        }
        seen.insert(fd.name.clone());
        check_conditions(fields, j, "visibleWhen", &fd.visible_when, &at, exact, out)?;
        check_conditions(fields, j, "requiredWhen", &fd.required_when, &at, exact, out)?;
        let Some(w) = lookup(&fd.widget) else {
            let names: Vec<&str> = WIDGETS.iter().map(|w| w.name.as_str()).collect();
            issue(
                out,
                format!("{at}.widget"),
                format!("unknown widget {} (known: {})", go_quote(&fd.widget), names.join(", ")),
            );
            continue;
        };
        if nested
            && (w.value == "objects" || w.value == "links" || w.name == NEW_NODE_WIDGET || w.name == DECISION_WIDGET)
        {
            issue(
                out,
                format!("{at}.widget"),
                format!("a group holds no {} field", w.name),
            );
        }
        if w.value == "objects" {
            if fd.fields.is_empty() {
                issue(out, format!("{at}.fields"), "a group needs at least one sub-field");
            }
            check_fields(&fd.fields, &format!("{at}.fields"), true, exact, out)?;
        } else if !fd.fields.is_empty() {
            issue(
                out,
                format!("{at}.fields"),
                format!("only a group has sub-fields, not a {}", w.name),
            );
        }
        for (k, v) in &fd.constraints {
            let Some(c) = w.constraint(k) else {
                issue(
                    out,
                    format!("{at}.constraints.{k}"),
                    format!("widget {} has no constraint {}", w.name, go_quote(k)),
                );
                continue;
            };
            if let Some(msg) = constraint_issue(&c.typ, v) {
                issue(out, format!("{at}.constraints.{k}"), msg);
                continue;
            }
            if c.typ == "field" {
                let r = v.as_str().unwrap_or_default();
                match fields.iter().find(|x| x.name == r) {
                    Some(other) if r != fd.name => {
                        if !c.widgets.is_empty() && !c.widgets.contains(&other.widget) {
                            issue(
                                out,
                                format!("{at}.constraints.{k}"),
                                format!(
                                    "field {} is a {}, not a {}",
                                    go_quote(r),
                                    other.widget,
                                    c.widgets.join(" or ")
                                ),
                            );
                        }
                    }
                    _ => issue(
                        out,
                        format!("{at}.constraints.{k}"),
                        format!("no other field {}", go_quote(r)),
                    ),
                }
            }
        }
        for c in &w.constraints {
            if c.required && !fd.constraints.contains_key(&c.name) {
                issue(
                    out,
                    format!("{at}.constraints"),
                    format!("widget {} needs the constraint {}", w.name, go_quote(&c.name)),
                );
            }
        }
        for msg in define(w, fd)? {
            issue(out, at.clone(), msg);
        }
        if let Some(d) = &fd.default
            && !matches!(w.value.as_str(), "none" | "links" | "objects" | "object")
            && let Some(first) = value_issues(w, fd, d)?.into_iter().next()
        {
            issue(out, format!("{at}.default"), first);
        }
    }
    Ok(())
}

fn check_conditions(
    fields: &[Field],
    j: usize,
    key: &str,
    cs: &[Condition],
    at: &str,
    exact: bool,
    out: &mut Vec<Issue>,
) -> Result<(), Unsure> {
    for (i, c) in cs.iter().enumerate() {
        if !exact && !c.in_list {
            return Err(Unsure("a form condition whose YAML may name an empty list"));
        }
        let p = format!("{at}.{key}[{i}]");
        if [c.equals, c.in_list, c.set].iter().filter(|x| **x).count() != 1 {
            issue(out, p.clone(), "a condition tests exactly one of equals, in or set");
        }
        match fields[..j].iter().rposition(|x| x.name == c.field) {
            None => issue(
                out,
                format!("{p}.field"),
                format!("no field {} before this one", go_quote(&c.field)),
            ),
            Some(k) => {
                if let Some(w) = lookup(&fields[k].widget)
                    && matches!(w.value.as_str(), "none" | "links" | "objects" | "object")
                {
                    issue(
                        out,
                        format!("{p}.field"),
                        format!(
                            "field {} is a {}: a condition tests a value",
                            go_quote(&c.field),
                            w.name
                        ),
                    );
                }
            }
        }
    }
    Ok(())
}

/// What a constraint of a type refuses of a value (`ConstraintType.check`).
fn constraint_issue(typ: &str, v: &Value) -> Option<&'static str> {
    match typ {
        "string" | "field" if v.as_str().is_none_or(str::is_empty) => Some("expects a non-empty string"),
        "number" if !v.is_number() => Some("expects a number"),
        "boolean" if !v.is_boolean() => Some("expects true or false"),
        "strings" if strings_of(v).is_none() => Some("expects a list of strings"),
        _ => None,
    }
}

fn strings_of(v: &Value) -> Option<Vec<&str>> {
    v.as_array()?.iter().map(Value::as_str).collect()
}

/// The checks of a field's definition beyond its constraints' types (`Widget.Define`).
fn define(w: &Widget, fd: &Field) -> Result<Vec<String>, Unsure> {
    Ok(match w.name.as_str() {
        "text" | "textarea" => {
            if let Some(p) = fd.constraints.get("pattern").and_then(Value::as_str)
                && regex::Regex::new(&format!("^(?:{p})$")).is_err()
            {
                return Err(Unsure("a pattern Go's regexp words the refusal of"));
            }
            Vec::new()
        }
        "select" | "multiselect" => match fd.constraints.get("options") {
            Some(o) if strings_of(o).is_none_or(|l| l.is_empty()) => {
                vec!["options must list at least one value".into()]
            }
            _ => Vec::new(),
        },
        LINK_WIDGET => {
            let from = fd.constraints.contains_key("from");
            let key = fd.constraints.contains_key("fromKey");
            match (from, key) {
                (true, true) => vec!["a link field names its source by from or by fromKey, not both".into()],
                (false, false) => vec!["a link field names its source: from (a node field) or fromKey".into()],
                _ => Vec::new(),
            }
        }
        _ => Vec::new(),
    })
}

fn multiple(fd: &Field) -> bool {
    fd.constraints.get("multiple").and_then(Value::as_bool).unwrap_or(false)
}

/// What a widget refuses of a value given no environment (`Widget.check` with `Env{}`), for a default.
fn value_issues(w: &Widget, fd: &Field, v: &Value) -> Result<Vec<String>, Unsure> {
    let kind = match w.value.as_str() {
        "string" if !v.is_string() => Some("expects a text"),
        "number" if !v.is_number() => Some("expects a number"),
        "boolean" if !v.is_boolean() => Some("expects true or false"),
        "list" if strings_of(v).is_none() => Some("expects a list of texts"),
        "node" if multiple(fd) && strings_of(v).is_none() => Some("expects a list of node keys"),
        "node" if !multiple(fd) && !v.is_string() => Some("expects a node key"),
        _ => None,
    };
    if let Some(k) = kind {
        return Ok(vec![k.into()]);
    }
    let options = || fd.constraints.get("options").and_then(strings_of).unwrap_or_default();
    Ok(match w.name.as_str() {
        "boolean" => Vec::new(),
        "text" | "textarea"
            if !["minLength", "maxLength", "pattern"]
                .iter()
                .any(|k| fd.constraints.contains_key(*k)) =>
        {
            Vec::new()
        }
        "select" => {
            let s = v.as_str().unwrap_or_default();
            if options().contains(&s) {
                Vec::new()
            } else {
                vec![format!("{} is not one of the options", go_quote(s))]
            }
        }
        "multiselect" => {
            let opts = options();
            let list = strings_of(v).unwrap_or_default();
            let mut out: Vec<String> = list
                .iter()
                .filter(|s| !opts.contains(s))
                .map(|s| format!("{} is not one of the options", go_quote(s)))
                .collect();
            out.extend(count(fd, list.len(), "values")?);
            out
        }
        "node" => {
            let keys: Vec<&str> = if multiple(fd) {
                strings_of(v).unwrap_or_default()
            } else {
                vec![v.as_str().unwrap_or_default()]
            };
            let mut out = Vec::new();
            let mut seen = HashSet::new();
            for k in &keys {
                if !seen.insert(*k) {
                    out.push(format!("{k} is given twice"));
                } else if k.is_empty() {
                    out.push("empty node key".into());
                }
            }
            if multiple(fd) {
                out.extend(count(fd, keys.len(), "nodes")?);
            }
            out
        }
        _ => return Err(Unsure("a default Go's widget checks in words of its own")),
    })
}

/// The bounds of a count (`checkCount`).
fn count(fd: &Field, n: usize, what: &str) -> Result<Vec<String>, Unsure> {
    let mut out = Vec::new();
    for (k, more) in [("min", false), ("max", true)] {
        let Some(m) = fd.constraints.get(k).and_then(Value::as_f64) else {
            continue;
        };
        let n = n as f64;
        if (more && n > m) || (!more && n < m) {
            let word = if more { "at most" } else { "at least" };
            out.push(format!("{word} {} {what}", go_g(m)?));
        }
    }
    Ok(out)
}

/// A number as Go's `%g` prints it, where it prints no exponent.
fn go_g(f: f64) -> Result<String, Unsure> {
    if f == 0.0 || (1e-4..1e6).contains(&f.abs()) {
        Ok(format!("{f}"))
    } else {
        Err(Unsure("a bound Go prints with an exponent"))
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn checks_a_form_as_go() {
        assert!(WIDGETS.iter().any(|w| w.name == "text"));
        let f = fields(&json!({"fields": [
            {"name": "title", "widget": "text", "constraints": {"maxLength": 200}},
            {"name": "title", "widget": "nope"},
            {"name": "p", "widget": "select", "constraints": {"options": []}, "default": "x"},
            {"name": "l", "widget": "link", "constraints": {"fromKey": "$self", "bad": 1}},
            {"name": "m", "widget": "markdown"},
        ]}));
        let got: Vec<String> = check(&f, true)
            .unwrap()
            .into_iter()
            .map(|i| format!("{}: {}", i.path, i.message))
            .collect();
        assert_eq!(
            got,
            [
                "fields[1].name: duplicate field \"title\"",
                "fields[1].widget: unknown widget \"nope\" (known: boolean, date, decision, file, group, link, markdown, multiselect, newNode, node, number, select, text, textarea)",
                "fields[2]: options must list at least one value",
                "fields[2].default: \"x\" is not one of the options",
                "fields[3].constraints.bad: widget link has no constraint \"bad\"",
                "fields[4].constraints: widget markdown needs the constraint \"text\"",
            ]
        );
    }
}
