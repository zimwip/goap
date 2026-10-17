//! The widgets: their values, their constraints, the registry and the built-in ones (`widgets.go`).
use std::{
    collections::{BTreeMap, HashSet},
    fmt,
    sync::{Arc, LazyLock},
};

use parking_lot::RwLock;
use regex::Regex;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::form::{
    Field, NewNode, constraint_str, fmt_g, keys_of, objects_of, quote, sorted_keys, str_of,
    strings_of,
};

/// The shape of the value a widget produces.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ValueKind {
    String,
    Number,
    Boolean,
    /// A list of strings.
    List,
    /// A node key, or a list of keys when the field allows several.
    Node,
    /// A list of links {from, type, to}.
    Links,
    /// An object, or a list of objects when the field allows several.
    Object,
    /// A list of objects, the values of the sub-fields of a group.
    Objects,
    /// A display widget, it produces no value.
    None,
    /// Any JSON value, checked by the widget only (the kind of a widget registered without one).
    #[default]
    Any,
}

impl ValueKind {
    /// The name of the kind.
    pub fn as_str(self) -> &'static str {
        match self {
            ValueKind::String => "string",
            ValueKind::Number => "number",
            ValueKind::Boolean => "boolean",
            ValueKind::List => "list",
            ValueKind::Node => "node",
            ValueKind::Links => "links",
            ValueKind::Object => "object",
            ValueKind::Objects => "objects",
            ValueKind::None => "none",
            ValueKind::Any => "any",
        }
    }
}

impl fmt::Display for ValueKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The type of the value of a constraint.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ConstraintType {
    String,
    Number,
    Boolean,
    Strings,
    /// Names another field of the form.
    Field,
}

impl ConstraintType {
    /// The name of the type.
    pub fn as_str(self) -> &'static str {
        match self {
            ConstraintType::String => "string",
            ConstraintType::Number => "number",
            ConstraintType::Boolean => "boolean",
            ConstraintType::Strings => "strings",
            ConstraintType::Field => "field",
        }
    }

    pub(crate) fn check(self, v: &Value) -> String {
        let bad = match self {
            ConstraintType::String | ConstraintType::Field => {
                !v.as_str().is_some_and(|s| !s.is_empty())
            }
            ConstraintType::Number => number(v).is_none(),
            ConstraintType::Boolean => !v.is_boolean(),
            ConstraintType::Strings => strings_of(Some(v)).is_none(),
        };
        if !bad {
            return String::new();
        }
        match self {
            ConstraintType::String | ConstraintType::Field => "expects a non-empty string",
            ConstraintType::Number => "expects a number",
            ConstraintType::Boolean => "expects true or false",
            ConstraintType::Strings => "expects a list of strings",
        }
        .into()
    }
}

impl fmt::Display for ConstraintType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// A constraint a widget accepts.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Constraint {
    pub name: String,
    #[serde(rename = "type")]
    pub r#type: ConstraintType,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub required: bool,
    /// The widgets the field named by a `Field` constraint may have (none: any).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub widgets: Vec<String>,
}

impl Constraint {
    /// A constraint of that name and type, optional, described.
    pub fn new(name: &str, r#type: ConstraintType, description: &str) -> Constraint {
        Constraint {
            name: name.into(),
            r#type,
            description: description.into(),
            required: false,
            widgets: Vec::new(),
        }
    }
}

/// What a form needs to know of a node a field names.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Node {
    pub key: String,
    pub r#type: String,
    pub state: String,
    pub namespace: String,
    /// The change touches the node (an impact of the change).
    pub in_change: bool,
}

/// Finds a node by key.
pub type NodeFn = Arc<dyn Fn(&str) -> Option<Node> + Send + Sync>;
/// Reports whether a type is the other one or one of its subtypes.
pub type IsAFn = Arc<dyn Fn(&str, &str) -> bool + Send + Sync>;
/// Finds a decision point of the change by id.
pub type DecisionFn = Arc<dyn Fn(&str) -> Option<Decision> + Send + Sync>;

/// Resolves what the checks of the values need.
#[derive(Clone, Default)]
pub struct Env {
    /// Finds a node by key (`None`: the nodes are not checked).
    pub node: Option<NodeFn>,
    /// Reports whether a type is the other one or one of its subtypes (`None`: equality).
    pub is_a: Option<IsAFn>,
    /// Finds a decision point of the change by id (`None`: the points are not checked).
    pub decision: Option<DecisionFn>,
}

impl fmt::Debug for Env {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Env")
            .field("node", &self.node.is_some())
            .field("is_a", &self.is_a.is_some())
            .field("decision", &self.decision.is_some())
            .finish()
    }
}

/// What a form needs to know of a decision point of the change.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Decision {
    pub id: String,
    pub question: String,
    /// The options the point is about (ids; none: any option).
    pub options: Vec<String>,
    /// The point is settled, nobody rules it any more.
    pub decided: bool,
}

impl Env {
    /// Adds the nodes a form creates to the nodes the environment knows.
    pub(crate) fn with(&self, created: Vec<NewNode>) -> Env {
        let Some(known) = self.node.clone() else {
            return self.clone();
        };
        if created.is_empty() {
            return self.clone();
        }
        let by_key: BTreeMap<String, Node> = created
            .into_iter()
            .map(|n| {
                (
                    n.key.clone(),
                    Node {
                        key: n.key,
                        r#type: n.r#type,
                        in_change: true,
                        ..Node::default()
                    },
                )
            })
            .collect();
        let mut e = self.clone();
        e.node = Some(Arc::new(move |k: &str| {
            by_key.get(k).cloned().or_else(|| known(k))
        }));
        e
    }

    fn is_a(&self, typ: &str, sup: &str) -> bool {
        match &self.is_a {
            Some(f) => f(typ, sup),
            None => typ == sup,
        }
    }
}

/// Checks a field of a widget beyond its constraints' types.
pub type DefineCheck = Arc<dyn Fn(&Field) -> Vec<String> + Send + Sync>;
/// Checks a value of the right kind against the constraints of the field.
pub type ValueCheck =
    Arc<dyn Fn(&Field, &Value, &Env, &Map<String, Value>) -> Vec<String> + Send + Sync>;

/// A kind of field: its value and its constraints. The web renders it with the component registered under the same
/// name.
#[derive(Clone, Default, Serialize)]
pub struct Widget {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub value: ValueKind,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub constraints: Vec<Constraint>,
    /// Checks a field of the widget beyond its constraints' types (`None`: nothing more).
    #[serde(skip)]
    pub define: Option<DefineCheck>,
    /// Checks a value of the right kind against the constraints of the field (`None`: nothing more).
    #[serde(skip)]
    pub check: Option<ValueCheck>,
}

impl fmt::Debug for Widget {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Widget")
            .field("name", &self.name)
            .field("description", &self.description)
            .field("value", &self.value)
            .field("constraints", &self.constraints)
            .field("define", &self.define.is_some())
            .field("check", &self.check.is_some())
            .finish()
    }
}

impl Widget {
    /// A widget of that name producing any value, with no constraint and no check.
    pub fn new(name: &str) -> Widget {
        Widget {
            name: name.into(),
            ..Widget::default()
        }
    }

    pub(crate) fn constraint(&self, name: &str) -> Option<&Constraint> {
        self.constraints.iter().find(|c| c.name == name)
    }

    /// Checks a value: its kind, then the widget's own check.
    pub(crate) fn check(
        &self,
        f: &Field,
        v: &Value,
        env: &Env,
        values: &Map<String, Value>,
    ) -> Vec<String> {
        let v = crate::form::normalize(v);
        let msg = kind_issue(self.value, f, &v);
        if !msg.is_empty() {
            return vec![msg.into()];
        }
        match &self.check {
            Some(c) => c(f, &v, env, values),
            None => Vec::new(),
        }
    }
}

fn kind_issue(k: ValueKind, f: &Field, v: &Value) -> &'static str {
    let all_objects = |v: &Value| v.as_array().map(|l| l.iter().all(Value::is_object));
    match k {
        ValueKind::String if !v.is_string() => "expects a text",
        ValueKind::Number if number(v).is_none() => "expects a number",
        ValueKind::Boolean if !v.is_boolean() => "expects true or false",
        ValueKind::List if strings_of(Some(v)).is_none() => "expects a list of texts",
        ValueKind::Node => {
            if multiple(f) {
                if strings_of(Some(v)).is_none() {
                    return "expects a list of node keys";
                }
            } else if !v.is_string() {
                return "expects a node key";
            }
            ""
        }
        ValueKind::Object => {
            if multiple(f) {
                if all_objects(v) != Some(true) {
                    return "expects a list of objects";
                }
            } else if !v.is_object() {
                return "expects an object";
            }
            ""
        }
        ValueKind::Objects => match all_objects(v) {
            None => "expects a list of entries",
            Some(false) => "expects a list of entries {field: value}",
            Some(true) => "",
        },
        ValueKind::Links => match all_objects(v) {
            None => "expects a list of links",
            Some(false) => "expects a list of links {from, type, to}",
            Some(true) => "",
        },
        _ => "",
    }
}

static REGISTRY: LazyLock<RwLock<BTreeMap<String, Widget>>> = LazyLock::new(|| {
    let mut m = BTreeMap::new();
    for w in builtins() {
        insert(&mut m, w);
    }
    RwLock::new(m)
});

fn insert(m: &mut BTreeMap<String, Widget>, w: Widget) {
    if w.name.is_empty() {
        panic!("form: a widget needs a name");
    }
    if m.contains_key(&w.name) {
        panic!("form: widget {} registered twice", quote(&w.name));
    }
    m.insert(w.name.clone(), w);
}

/// Adds a widget to the registry; a duplicate or nameless widget panics.
pub fn register(w: Widget) {
    let mut m = REGISTRY.write();
    insert(&mut m, w);
}

/// Finds a widget by name.
pub fn lookup(name: &str) -> Option<Widget> {
    REGISTRY.read().get(name).cloned()
}

/// Lists the registered widgets, sorted.
pub fn names() -> Vec<String> {
    REGISTRY.read().keys().cloned().collect()
}

/// Lists the registered widgets, sorted by name.
pub fn all() -> Vec<Widget> {
    REGISTRY.read().values().cloned().collect()
}

// Names of the built-in widgets.
pub const WIDGET_TEXT: &str = "text";
pub const WIDGET_TEXTAREA: &str = "textarea";
pub const WIDGET_NUMBER: &str = "number";
pub const WIDGET_BOOLEAN: &str = "boolean";
pub const WIDGET_DATE: &str = "date";
pub const WIDGET_SELECT: &str = "select";
pub const WIDGET_MULTISELECT: &str = "multiselect";
pub const WIDGET_NODE: &str = "node";
pub const WIDGET_LINK: &str = "link";
pub const WIDGET_MARKDOWN: &str = "markdown";
pub const WIDGET_GROUP: &str = "group";
pub const WIDGET_NEW_NODE: &str = "newNode";
pub const WIDGET_DECISION: &str = "decision";
pub const WIDGET_FILE: &str = "file";

/// The largest file a file field accepts: the file travels in the values and stays in the artifact.
pub const MAX_FILE_BYTES: usize = 512 << 10;

fn widget(name: &str, description: &str, value: ValueKind, constraints: Vec<Constraint>) -> Widget {
    Widget {
        name: name.into(),
        description: description.into(),
        value,
        constraints,
        define: None,
        check: None,
    }
}

fn check_fn(f: fn(&Field, &Value, &Env, &Map<String, Value>) -> Vec<String>) -> Option<ValueCheck> {
    Some(Arc::new(f))
}

fn define_fn(f: fn(&Field) -> Vec<String>) -> Option<DefineCheck> {
    Some(Arc::new(f))
}

fn builtins() -> Vec<Widget> {
    use ConstraintType as T;
    let c = Constraint::new;
    let text_constraints = vec![
        c("minLength", T::Number, "fewest characters"),
        c("maxLength", T::Number, "most characters"),
        c(
            "pattern",
            T::String,
            "regular expression the whole text matches",
        ),
    ];
    let mut out = Vec::new();
    for (n, d) in [
        (WIDGET_TEXT, "a line of text"),
        (WIDGET_TEXTAREA, "a text of several lines"),
    ] {
        out.push(Widget {
            define: define_fn(define_pattern),
            check: check_fn(check_text),
            ..widget(n, d, ValueKind::String, text_constraints.clone())
        });
    }
    out.push(Widget {
        check: check_fn(check_number),
        ..widget(
            WIDGET_NUMBER,
            "a number",
            ValueKind::Number,
            vec![
                c("min", T::Number, ""),
                c("max", T::Number, ""),
                c("step", T::Number, ""),
                c("integer", T::Boolean, "whole numbers only"),
            ],
        )
    });
    out.push(widget(
        WIDGET_BOOLEAN,
        "yes or no",
        ValueKind::Boolean,
        vec![],
    ));
    out.push(Widget {
        check: check_fn(check_date),
        ..widget(
            WIDGET_DATE,
            "a date, YYYY-MM-DD",
            ValueKind::String,
            vec![
                c("min", T::String, "earliest date, YYYY-MM-DD"),
                c("max", T::String, "latest date, YYYY-MM-DD"),
            ],
        )
    });
    let options = Constraint {
        required: true,
        ..c("options", T::Strings, "the values offered")
    };
    out.push(Widget {
        define: define_fn(define_options),
        check: check_fn(check_select),
        ..widget(
            WIDGET_SELECT,
            "one value among options",
            ValueKind::String,
            vec![options.clone()],
        )
    });
    out.push(Widget {
        define: define_fn(define_options),
        check: check_fn(check_multiselect),
        ..widget(
            WIDGET_MULTISELECT,
            "values among options",
            ValueKind::List,
            vec![
                options,
                c("min", T::Number, "fewest values"),
                c("max", T::Number, "most values"),
            ],
        )
    });
    out.push(Widget {
        check: check_fn(check_node),
        ..widget(
            WIDGET_NODE,
            "a node of the graph, found by a search",
            ValueKind::Node,
            vec![
                c(
                    "types",
                    T::Strings,
                    "qualified node types offered (a subtype qualifies)",
                ),
                c("states", T::Strings, "lifecycle states offered"),
                c("namespace", T::String, "namespace of the nodes offered"),
                c("inChange", T::Boolean, "only the nodes the change touches"),
                c("multiple", T::Boolean, "several nodes"),
                c("min", T::Number, "fewest nodes (multiple)"),
                c("max", T::Number, "most nodes (multiple)"),
            ],
        )
    });
    out.push(Widget {
        define: define_fn(define_link),
        check: check_fn(check_links),
        ..widget(
            WIDGET_LINK,
            "links to create from a node to nodes found by a search",
            ValueKind::Links,
            vec![
                Constraint {
                    widgets: vec![WIDGET_NODE.into(), WIDGET_NEW_NODE.into()],
                    ..c(
                        "from",
                        T::Field,
                        "the node field (or new node field) whose node is the source",
                    )
                },
                c("fromKey", T::String, "a fixed source node"),
                c("linkTypes", T::Strings, "qualified link types offered"),
                c(
                    "toTypes",
                    T::Strings,
                    "qualified node types of the targets (a subtype qualifies)",
                ),
                c("toStates", T::Strings, "lifecycle states of the targets"),
                c("min", T::Number, "fewest links"),
                c("max", T::Number, "most links"),
            ],
        )
    });
    out.push(Widget {
        check: check_fn(|f, v, _, _| check_count(f, v.as_array().map_or(0, Vec::len), "entries")),
        ..widget(
            WIDGET_GROUP,
            "entries repeated by the person, each made of the sub-fields",
            ValueKind::Objects,
            vec![
                c("min", T::Number, "fewest entries"),
                c("max", T::Number, "most entries"),
            ],
        )
    });
    out.push(Widget {
        check: check_fn(check_new_nodes),
        ..widget(
            WIDGET_NEW_NODE,
            "a node the change creates: its type, key and properties",
            ValueKind::Object,
            vec![
                c(
                    "types",
                    T::Strings,
                    "qualified node types the person may create (a subtype qualifies)",
                ),
                c(
                    "properties",
                    T::Strings,
                    "the attributes asked (none: the name of the type)",
                ),
                c("multiple", T::Boolean, "several nodes"),
                c("min", T::Number, "fewest nodes (multiple)"),
                c("max", T::Number, "most nodes (multiple)"),
            ],
        )
    });
    out.push(Widget {
        check: check_fn(check_decision),
        ..widget(
            WIDGET_DECISION,
            "rules a decision point of the change: an option and why",
            ValueKind::Object,
            vec![c("justify", T::Boolean, "the justification is required")],
        )
    });
    out.push(Widget {
        check: check_fn(check_files),
        ..widget(
            WIDGET_FILE,
            "a file the person attaches, kept in the values",
            ValueKind::Object,
            vec![
                c(
                    "accept",
                    T::Strings,
                    "media types (text/*, application/pdf) or extensions (.csv) accepted",
                ),
                c(
                    "maxBytes",
                    T::Number,
                    &format!("largest file, in bytes (at most {MAX_FILE_BYTES})"),
                ),
                c("multiple", T::Boolean, "several files"),
                c("min", T::Number, "fewest files (multiple)"),
                c("max", T::Number, "most files (multiple)"),
            ],
        )
    });
    out.push(widget(
        WIDGET_MARKDOWN,
        "a text shown to the person",
        ValueKind::None,
        vec![Constraint {
            required: true,
            ..c("text", T::String, "the Markdown text")
        }],
    ));
    out
}

pub(crate) fn number(v: &Value) -> Option<f64> {
    v.as_f64()
}

fn multiple(f: &Field) -> bool {
    f.constraints
        .get("multiple")
        .and_then(Value::as_bool)
        .unwrap_or(false)
}

fn bound(f: &Field, name: &str) -> Option<f64> {
    f.constraints.get(name).and_then(number)
}

fn whole_regex(p: &str) -> Result<Regex, regex::Error> {
    Regex::new(&format!("^(?:{p})$"))
}

fn define_pattern(f: &Field) -> Vec<String> {
    if let Some(p) = f.constraints.get("pattern").and_then(Value::as_str)
        && let Err(e) = whole_regex(p)
    {
        return vec![format!("invalid pattern: {e}")];
    }
    Vec::new()
}

fn define_options(f: &Field) -> Vec<String> {
    if let Some(o) = f.constraints.get("options")
        && strings_of(Some(o)).is_none_or(|l| l.is_empty())
    {
        return vec!["options must list at least one value".into()];
    }
    Vec::new()
}

fn define_link(f: &Field) -> Vec<String> {
    let from = f.constraints.contains_key("from");
    let key = f.constraints.contains_key("fromKey");
    match (from, key) {
        (true, true) => {
            vec!["a link field names its source by from or by fromKey, not both".into()]
        }
        (false, false) => {
            vec!["a link field names its source: from (a node field) or fromKey".into()]
        }
        _ => Vec::new(),
    }
}

fn check_text(f: &Field, v: &Value, _: &Env, _: &Map<String, Value>) -> Vec<String> {
    let s = v.as_str().unwrap_or_default();
    let n = s.chars().count() as f64;
    let mut out = Vec::new();
    if let Some(m) = bound(f, "minLength")
        && n < m
    {
        out.push(format!("at least {} characters", fmt_g(m)));
    }
    if let Some(m) = bound(f, "maxLength")
        && n > m
    {
        out.push(format!("at most {} characters", fmt_g(m)));
    }
    if let Some(p) = f.constraints.get("pattern").and_then(Value::as_str)
        && let Ok(re) = whole_regex(p)
        && !re.is_match(s)
    {
        out.push(format!("does not match {p}"));
    }
    out
}

fn check_number(f: &Field, v: &Value, _: &Env, _: &Map<String, Value>) -> Vec<String> {
    let x = number(v).unwrap_or_default();
    let mut out = Vec::new();
    if let Some(m) = bound(f, "min")
        && x < m
    {
        out.push(format!("at least {}", fmt_g(m)));
    }
    if let Some(m) = bound(f, "max")
        && x > m
    {
        out.push(format!("at most {}", fmt_g(m)));
    }
    if f.constraints.get("integer").and_then(Value::as_bool) == Some(true) && x != (x as i64) as f64
    {
        out.push("a whole number".into());
    }
    out
}

/// Go's `time.Parse(time.DateOnly, s)` succeeds: YYYY-MM-DD, a day of the month.
fn is_date(s: &str) -> bool {
    let b = s.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return false;
    }
    let digits = |r: std::ops::Range<usize>| {
        b[r.clone()]
            .iter()
            .all(u8::is_ascii_digit)
            .then(|| s[r].parse::<u32>().ok())
            .flatten()
    };
    let (Some(y), Some(m), Some(d)) = (digits(0..4), digits(5..7), digits(8..10)) else {
        return false;
    };
    let leap = y.is_multiple_of(4) && (!y.is_multiple_of(100) || y.is_multiple_of(400));
    let days = match m {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if leap => 29,
        2 => 28,
        _ => return false,
    };
    (1..=days).contains(&d)
}

fn check_date(f: &Field, v: &Value, _: &Env, _: &Map<String, Value>) -> Vec<String> {
    let s = v.as_str().unwrap_or_default();
    if !is_date(s) {
        return vec!["expects a date YYYY-MM-DD".into()];
    }
    let mut out = Vec::new();
    if let Some(m) = f.constraints.get("min").and_then(Value::as_str)
        && s < m
    {
        out.push(format!("on or after {m}"));
    }
    if let Some(m) = f.constraints.get("max").and_then(Value::as_str)
        && s > m
    {
        out.push(format!("on or before {m}"));
    }
    out
}

fn options_of(f: &Field) -> Vec<String> {
    strings_of(f.constraints.get("options")).unwrap_or_default()
}

fn check_select(f: &Field, v: &Value, _: &Env, _: &Map<String, Value>) -> Vec<String> {
    let s = v.as_str().unwrap_or_default();
    if !options_of(f).iter().any(|o| o == s) {
        return vec![format!("{} is not one of the options", quote(s))];
    }
    Vec::new()
}

fn check_count(f: &Field, n: usize, what: &str) -> Vec<String> {
    let mut out = Vec::new();
    if let Some(m) = bound(f, "min")
        && (n as f64) < m
    {
        out.push(format!("at least {} {what}", fmt_g(m)));
    }
    if let Some(m) = bound(f, "max")
        && (n as f64) > m
    {
        out.push(format!("at most {} {what}", fmt_g(m)));
    }
    out
}

fn check_multiselect(f: &Field, v: &Value, _: &Env, _: &Map<String, Value>) -> Vec<String> {
    let opts = options_of(f);
    let list = strings_of(Some(v)).unwrap_or_default();
    let mut out: Vec<String> = list
        .iter()
        .filter(|s| !opts.contains(s))
        .map(|s| format!("{} is not one of the options", quote(s)))
        .collect();
    out.extend(check_count(f, list.len(), "values"));
    out
}

/// Checks one node key against type and state constraints.
fn node_issue(
    key: &str,
    types: &[String],
    states: &[String],
    namespace: &str,
    in_change: bool,
    env: &Env,
) -> String {
    if key.is_empty() {
        return "empty node key".into();
    }
    let Some(find) = &env.node else {
        return String::new();
    };
    let Some(n) = find(key) else {
        return format!("unknown node {}", quote(key));
    };
    if !types.is_empty() && !types.iter().any(|t| env.is_a(&n.r#type, t)) {
        return format!("{key} is a {}, not one of {}", n.r#type, types.join(", "));
    }
    if !states.is_empty() && !states.contains(&n.state) {
        return format!(
            "{key} is {}, not one of {}",
            state_name(&n.state),
            states.join(", ")
        );
    }
    if !namespace.is_empty() && n.namespace != namespace {
        return format!("{key} is not in the namespace {namespace}");
    }
    if in_change && !n.in_change {
        return format!("{key} is not a node of the change");
    }
    String::new()
}

fn state_name(s: &str) -> String {
    if s.is_empty() {
        "in no state".into()
    } else {
        format!("in the state {s}")
    }
}

fn check_node(f: &Field, v: &Value, env: &Env, _: &Map<String, Value>) -> Vec<String> {
    let keys = if multiple(f) {
        strings_of(Some(v)).unwrap_or_default()
    } else {
        vec![v.as_str().unwrap_or_default().to_string()]
    };
    let types = strings_of(f.constraints.get("types")).unwrap_or_default();
    let states = strings_of(f.constraints.get("states")).unwrap_or_default();
    let ns = constraint_str(f, "namespace");
    let in_change = f
        .constraints
        .get("inChange")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    let mut out = Vec::new();
    let mut seen = HashSet::new();
    for k in &keys {
        if !seen.insert(k.as_str()) {
            out.push(format!("{k} is given twice"));
            continue;
        }
        let msg = node_issue(k, &types, &states, ns, in_change, env);
        if !msg.is_empty() {
            out.push(msg);
        }
    }
    if multiple(f) {
        out.extend(check_count(f, keys.len(), "nodes"));
    }
    out
}

fn check_links(f: &Field, v: &Value, env: &Env, values: &Map<String, Value>) -> Vec<String> {
    let empty = Vec::new();
    let list = v.as_array().unwrap_or(&empty);
    let link_types = strings_of(f.constraints.get("linkTypes")).unwrap_or_default();
    let to_types = strings_of(f.constraints.get("toTypes")).unwrap_or_default();
    let to_states = strings_of(f.constraints.get("toStates")).unwrap_or_default();
    let sources: Vec<String> = {
        let k = constraint_str(f, "fromKey");
        let reference = constraint_str(f, "from");
        if !k.is_empty() {
            vec![k.to_string()]
        } else if !reference.is_empty() {
            keys_of(values.get(reference).unwrap_or(&Value::Null))
        } else {
            Vec::new()
        }
    };
    let mut out = Vec::new();
    let mut seen = HashSet::new();
    let no = Map::new();
    for (i, e) in list.iter().enumerate() {
        let m = e.as_object().unwrap_or(&no);
        let (from, typ, to) = (str_of(m, "from"), str_of(m, "type"), str_of(m, "to"));
        let at = format!("link {}: ", i + 1);
        if from.is_empty() || typ.is_empty() || to.is_empty() {
            out.push(format!("{at}needs from, type and to"));
            continue;
        }
        if !sources.iter().any(|s| s == from) {
            if sources.is_empty() {
                out.push(format!("{at}no source node is chosen"));
            } else {
                out.push(format!("{at}source {from} is not {}", sources.join(" or ")));
            }
            continue;
        }
        if !link_types.is_empty() && !link_types.iter().any(|t| t == typ) {
            out.push(format!("{at}{typ} is not one of {}", link_types.join(", ")));
            continue;
        }
        if !seen.insert(format!("{from}|{typ}|{to}")) {
            out.push(format!("{at}given twice"));
            continue;
        }
        let msg = node_issue(to, &to_types, &to_states, "", false, env);
        if !msg.is_empty() {
            out.push(format!("{at}{msg}"));
        }
    }
    out.extend(check_count(f, list.len(), "links"));
    out
}

/// The shape of the key of a node a form creates.
static KEY_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[A-Za-z0-9][A-Za-z0-9_.:-]*$").unwrap());

fn check_new_nodes(f: &Field, v: &Value, env: &Env, _: &Map<String, Value>) -> Vec<String> {
    let entries = objects_of(v);
    let types = strings_of(f.constraints.get("types")).unwrap_or_default();
    let asked = strings_of(f.constraints.get("properties")).unwrap_or_default();
    let mut out = Vec::new();
    let mut seen = HashSet::new();
    for (i, m) in entries.iter().enumerate() {
        let at = if multiple(f) {
            format!("node {}: ", i + 1)
        } else {
            String::new()
        };
        let (key, typ) = (str_of(m, "key"), str_of(m, "type"));
        if typ.is_empty() {
            out.push(format!("{at}needs a type"));
        } else if !types.is_empty() && !types.iter().any(|t| env.is_a(typ, t)) {
            out.push(format!("{at}{typ} is not one of {}", types.join(", ")));
        }
        if key.is_empty() {
            out.push(format!("{at}needs a key"));
        } else if !KEY_RE.is_match(key) {
            out.push(format!(
                "{at}invalid key {} (letters, digits, _ . : -)",
                quote(key)
            ));
        } else if seen.contains(key) {
            out.push(format!("{at}{key} is given twice"));
        } else if let Some(find) = &env.node
            && find(key).is_some()
        {
            out.push(format!("{at}{key} already exists"));
        }
        seen.insert(key);
        if let Some(p) = m.get("props") {
            match p.as_object() {
                None => out.push(format!("{at}props is an object of properties")),
                Some(props) => {
                    for k in sorted_keys(props) {
                        if !asked.is_empty() && !asked.contains(&k) {
                            out.push(format!("{at}{k} is not a property asked"));
                        }
                    }
                }
            }
        }
        for k in sorted_keys(m) {
            if k != "key" && k != "type" && k != "props" {
                out.push(format!("{at}unknown entry {}", quote(&k)));
            }
        }
    }
    if multiple(f) {
        out.extend(check_count(f, entries.len(), "nodes"));
    }
    out
}

fn check_decision(f: &Field, v: &Value, env: &Env, _: &Map<String, Value>) -> Vec<String> {
    let no = Map::new();
    let m = v.as_object().unwrap_or(&no);
    let (point, option, why) = (
        str_of(m, "point"),
        str_of(m, "option"),
        str_of(m, "justification"),
    );
    let mut out = Vec::new();
    if point.is_empty() {
        out.push("needs the decision point".to_string());
    }
    if option.is_empty() {
        out.push("needs an option".into());
    }
    if f.constraints.get("justify").and_then(Value::as_bool) == Some(true) && why.trim().is_empty()
    {
        out.push("needs a justification".into());
    }
    let Some(find) = &env.decision else {
        return out;
    };
    if !out.is_empty() {
        return out;
    }
    match find(point) {
        None => out.push(format!("unknown decision point {}", quote(point))),
        Some(d) if d.decided => out.push(format!(
            "the decision point {} is already decided",
            quote(&d.question)
        )),
        Some(d) if !d.options.is_empty() && !d.options.iter().any(|o| o == option) => {
            out.push(format!(
                "{} is not an option of {}",
                quote(option),
                quote(&d.question)
            ))
        }
        Some(_) => {}
    }
    out
}

/// Go's `base64.StdEncoding.DecodeString`: padded, the line breaks skipped, the trailing bits not checked; the length
/// of the decoded bytes.
fn base64_len(s: &str) -> Option<usize> {
    let b: Vec<u8> = s.bytes().filter(|&c| c != b'\r' && c != b'\n').collect();
    if !b.len().is_multiple_of(4) {
        return None;
    }
    let mut n = 0;
    for (q, chunk) in b.chunks(4).enumerate() {
        let last = q == b.len() / 4 - 1;
        let pad = chunk.iter().rev().take_while(|&&c| c == b'=').count();
        if pad > 2 || (pad > 0 && !last) {
            return None;
        }
        let ok = chunk[..4 - pad]
            .iter()
            .all(|&c| c.is_ascii_alphanumeric() || c == b'+' || c == b'/');
        if !ok {
            return None;
        }
        n += 3 - pad;
    }
    Some(n)
}

fn check_files(f: &Field, v: &Value, _: &Env, _: &Map<String, Value>) -> Vec<String> {
    let entries = objects_of(v);
    let mut limit = MAX_FILE_BYTES as f64;
    if let Some(m) = bound(f, "maxBytes")
        && m < limit
    {
        limit = m;
    }
    let accept = strings_of(f.constraints.get("accept")).unwrap_or_default();
    let mut out = Vec::new();
    for (i, m) in entries.iter().enumerate() {
        let at = if multiple(f) {
            format!("file {}: ", i + 1)
        } else {
            String::new()
        };
        let (name, typ, content) = (str_of(m, "name"), str_of(m, "type"), str_of(m, "content"));
        let raw = base64_len(content);
        if name.is_empty() {
            out.push(format!("{at}needs a name"));
        } else if raw.is_none() {
            out.push(format!("{at}content is not base64"));
        } else if raw.is_some_and(|n| n as f64 > limit) {
            out.push(format!("{at}{name} is larger than {} bytes", fmt_g(limit)));
        } else if !accept.is_empty() && !accepts(&accept, name, typ) {
            out.push(format!("{at}{name} is not one of {}", accept.join(", ")));
        }
    }
    if multiple(f) {
        out.extend(check_count(f, entries.len(), "files"));
    }
    out
}

/// Go's `path.Ext`: the extension of the last element of a slash-separated path.
fn ext(name: &str) -> &str {
    for (i, c) in name.char_indices().rev() {
        match c {
            '/' => return "",
            '.' => return &name[i..],
            _ => {}
        }
    }
    ""
}

/// Reports whether a file matches one of the accepted media types (text/*) or extensions (.csv).
fn accepts(accept: &[String], name: &str, typ: &str) -> bool {
    let ext = ext(name).to_lowercase();
    let typ = typ.to_lowercase();
    for a in accept {
        let a = a.trim().to_lowercase();
        if a.starts_with('.') {
            if ext == a {
                return true;
            }
        } else if let Some(prefix) = a.strip_suffix('*')
            && a.ends_with("/*")
        {
            if typ.starts_with(prefix) {
                return true;
            }
        } else if a == typ {
            return true;
        }
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dates_and_base64() {
        assert!(is_date("2024-02-29") && !is_date("2023-02-29") && !is_date("2024-1-05"));
        assert_eq!(base64_len("YSxiCg=="), Some(4));
        assert_eq!(base64_len("YWJjZGVmZ2hpams="), Some(11));
        assert_eq!(base64_len("YQ"), None);
        assert_eq!(base64_len(""), Some(0));
        assert_eq!(ext("a/b.tar.gz"), ".gz");
        assert_eq!(ext("a.b/c"), "");
    }
}
