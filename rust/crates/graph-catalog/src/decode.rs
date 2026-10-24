//! A domain definition read as `def.ParseDomain` reads it and written as `json.Marshal` writes it: the first YAML (or
//! JSON) document decoded by yaml.v3's rules into the Go structs of [`shape`](crate::shape) (fields known by their YAML
//! name, unknown ones refused but within a type decoding its short form itself, the type errors gathered with their
//! line, the fatal ones stopping the read), then encoded as encoding/json does (the fields in their order, the empty
//! ones left out where the tag says so, maps sorted by key).
use std::collections::{BTreeMap, HashSet};

use base64::Engine;

use crate::{
    shape::{SHAPE, Type},
    yamlv3::{
        self, BINARY_TAG, Fatal, Id, Kind, MAP_TAG, MERGE_TAG, NULL_TAG, Resolved, SEQ_TAG, STR_TAG, Timestamp, Tree,
    },
};

/// A Go value of the shape.
#[derive(Debug, Clone, PartialEq)]
pub enum Value {
    Str(String),
    Bool(bool),
    Int(i64),
    Uint(u64),
    Float(f64),
    Time(Timestamp),
    /// a nil pointer, slice, map or interface
    Nil,
    Slice(Vec<Value>),
    Map(BTreeMap<String, Value>),
    /// a `map[interface{}]interface{}` (a mapping whose keys are not all strings, in an `interface{}`)
    GeneralMap(Vec<(Value, Value)>),
    /// the fields of a struct, in the order of the shape
    Struct(Vec<Value>),
}

impl Value {
    /// The zero value of a type.
    pub fn zero(t: &Type) -> Value {
        match t.kind.as_str() {
            "string" => Value::Str(String::new()),
            "bool" => Value::Bool(false),
            "int" => Value::Int(0),
            "float" => Value::Float(0.0),
            "struct" => Value::Struct(
                SHAPE
                    .strukt(&t.name)
                    .fields
                    .iter()
                    .map(|f| Value::zero(&f.typ))
                    .collect(),
            ),
            _ => Value::Nil,
        }
    }
}

/// Why a definition does not read: Go's error, as `ParseDomain` words it (`parse domain: …`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParseError(pub String);

/// The root value of a YAML (or JSON) definition (`def.ParseDomain`).
pub fn parse_domain(src: &[u8]) -> Result<Value, ParseError> {
    let fail = |m: String| ParseError(format!("parse domain: {m}"));
    let Some(tree) = yamlv3::parse_first(src).map_err(|f| fail(f.0))? else {
        return Err(fail("EOF".into()));
    };
    let root = Type {
        kind: "struct".into(),
        name: SHAPE.root.clone(),
        go: SHAPE.root.clone(),
        elem: None,
    };
    let mut d = Decoder::new(&tree, true);
    let mut out = Value::zero(&root);
    d.unmarshal(0, &root, &mut out).map_err(|f| fail(f.0))?;
    if !d.terrors.is_empty() {
        return Err(fail(format!("yaml: unmarshal errors:\n  {}", d.terrors.join("\n  "))));
    }
    Ok(out)
}

/// A key of a mapping a merge leaves alone (`mergedFields`): the key as an `interface{}` decodes it.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct MergeKey(String);

struct Decoder<'t> {
    tree: &'t Tree,
    terrors: Vec<String>,
    known_fields: bool,
    aliases: HashSet<Id>,
    merged: Option<HashSet<MergeKey>>,
}

/// The name an error gives the type a value is decoded into: the Go type, `def.plain` for a type decoding its short
/// form itself (`type plain T`).
fn go_name(t: &Type, plain: bool) -> String {
    if plain { "def.plain".into() } else { t.go.clone() }
}

impl<'t> Decoder<'t> {
    fn new(tree: &'t Tree, known_fields: bool) -> Self {
        Decoder {
            tree,
            terrors: Vec::new(),
            known_fields,
            aliases: HashSet::new(),
            merged: None,
        }
    }

    fn unmarshal(&mut self, id: Id, t: &Type, out: &mut Value) -> Result<bool, Fatal> {
        self.unmarshal_as(id, t, out, false)
    }

    /// `decoder.unmarshal`; `plain`: the value is a short-form type decoded as itself (no `UnmarshalYAML` again).
    fn unmarshal_as(&mut self, id: Id, t: &Type, out: &mut Value, plain: bool) -> Result<bool, Fatal> {
        let n = self.tree.node(id);
        match n.kind {
            Kind::Document => {
                if n.content.len() == 1 {
                    self.unmarshal_as(n.content[0], t, out, plain)?;
                    return Ok(true);
                }
                return Ok(false);
            }
            Kind::Alias => {
                if !self.aliases.insert(id) {
                    return Err(Fatal::new(format!("anchor '{}' value contains itself", n.value)));
                }
                let target = n.alias.expect("an alias names its node");
                let good = self.unmarshal_as(target, t, out, plain);
                self.aliases.remove(&id);
                return good;
            }
            _ => {}
        }
        // prepare: a pointer allocated, a type decoding its short form called
        let mut t = t;
        if self.tree.short_tag(id) != NULL_TAG {
            if t.kind == "ptr" {
                t = t.elem();
                if *out == Value::Nil {
                    *out = Value::zero(t);
                }
            }
            if !plain && t.kind == "struct" && !SHAPE.strukt(&t.name).short.is_empty() {
                return self.short(id, t, out);
            }
        }
        match n.kind {
            Kind::Scalar => self.scalar(id, t, out, plain),
            Kind::Mapping => self.mapping(id, t, out, plain),
            Kind::Sequence => self.sequence(id, t, out, plain),
            _ => unreachable!("documents and aliases are taken above"),
        }
    }

    /// `UnmarshalYAML` of a short-form type: a scalar sets its field, anything else is decoded as the type itself by a
    /// decoder of its own (`Node.Decode`: no field check), whose type errors are the caller's.
    fn short(&mut self, id: Id, t: &Type, out: &mut Value) -> Result<bool, Fatal> {
        let n = self.tree.node(id);
        let s = SHAPE.strukt(&t.name);
        if n.kind == Kind::Scalar {
            let i = s
                .fields
                .iter()
                .position(|f| f.yaml == s.short)
                .expect("the short field");
            if let Value::Struct(fields) = out {
                fields[i] = Value::Str(n.value.clone());
            }
            return Ok(true);
        }
        let mut d = Decoder::new(self.tree, false);
        d.unmarshal_as(id, t, out, true)?;
        if d.terrors.is_empty() {
            return Ok(true);
        }
        self.terrors.append(&mut d.terrors);
        Ok(false)
    }

    fn terror(&mut self, id: Id, tag: &str, t: &Type, plain: bool) {
        let n = self.tree.node(id);
        let tag = if n.tag.is_empty() {
            tag.to_string()
        } else {
            n.tag.clone()
        };
        let value = if tag != SEQ_TAG && tag != MAP_TAG {
            if n.value.len() > 10 {
                format!(" `{}...`", String::from_utf8_lossy(&n.value.as_bytes()[..7]))
            } else {
                format!(" `{}`", n.value)
            }
        } else {
            String::new()
        };
        self.terrors.push(format!(
            "line {}: cannot unmarshal {}{} into {}",
            n.line,
            yamlv3::short_tag(&tag),
            value,
            go_name(t, plain)
        ));
    }

    fn null(t: &Type, out: &mut Value) -> bool {
        match t.kind.as_str() {
            "any" | "ptr" | "map" | "slice" => {
                *out = Value::Nil;
                true
            }
            _ => false,
        }
    }

    fn scalar(&mut self, id: Id, t: &Type, out: &mut Value, plain: bool) -> Result<bool, Fatal> {
        let n = self.tree.node(id);
        let (tag, resolved) = if self.tree.indicated_string(id) {
            (STR_TAG.to_string(), Resolved::Str(n.value.clone()))
        } else {
            let (tag, r) = yamlv3::resolve(&n.tag, &n.value)?;
            if tag == BINARY_TAG {
                let Resolved::Str(s) = r else {
                    unreachable!("binary is not resolved")
                };
                let data = base64::engine::general_purpose::STANDARD
                    .decode(s.as_bytes())
                    .map_err(|_| Fatal::new("!!binary value contains invalid base64 data"))?;
                (tag, Resolved::Str(String::from_utf8_lossy(&data).into_owned()))
            } else {
                (tag, r)
            }
        };
        if resolved == Resolved::Null {
            return Ok(Self::null(t, out));
        }
        let set = |out: &mut Value, v: Value| {
            *out = v;
            Ok(true)
        };
        match t.kind.as_str() {
            "string" => {
                let s = match (&resolved, tag == BINARY_TAG) {
                    (Resolved::Str(s), true) => s.clone(),
                    _ => n.value.clone(),
                };
                return set(out, Value::Str(s));
            }
            "any" => {
                let v = match resolved {
                    Resolved::Bool(b) => Value::Bool(b),
                    Resolved::Int(i) => Value::Int(i),
                    Resolved::Uint(u) => Value::Uint(u),
                    Resolved::Float(f) => Value::Float(f),
                    Resolved::Str(s) => Value::Str(s),
                    Resolved::Time(ts) => Value::Time(ts),
                    Resolved::Null => Value::Nil,
                };
                return set(out, v);
            }
            "int" => match resolved {
                Resolved::Int(i) => return set(out, Value::Int(i)),
                Resolved::Uint(u) if i64::try_from(u).is_ok() => return set(out, Value::Int(u as i64)),
                Resolved::Float(f) if f <= i64::MAX as f64 => return set(out, Value::Int(f as i64)),
                _ => {}
            },
            "bool" => match resolved {
                Resolved::Bool(b) => return set(out, Value::Bool(b)),
                Resolved::Str(ref s) => match s.as_str() {
                    "y" | "Y" | "yes" | "Yes" | "YES" | "on" | "On" | "ON" => return set(out, Value::Bool(true)),
                    "n" | "N" | "no" | "No" | "NO" | "off" | "Off" | "OFF" => return set(out, Value::Bool(false)),
                    _ => {}
                },
                _ => {}
            },
            "float" => match resolved {
                Resolved::Int(i) => return set(out, Value::Float(i as f64)),
                Resolved::Uint(u) => return set(out, Value::Float(u as f64)),
                Resolved::Float(f) => return set(out, Value::Float(f)),
                _ => {}
            },
            _ => {}
        }
        self.terror(id, &tag, t, plain);
        Ok(false)
    }

    fn sequence(&mut self, id: Id, t: &Type, out: &mut Value, plain: bool) -> Result<bool, Fatal> {
        let any = Type {
            kind: "any".into(),
            name: String::new(),
            go: "interface {}".into(),
            elem: None,
        };
        let elem = match t.kind.as_str() {
            "slice" => t.elem(),
            "any" => &any,
            _ => {
                self.terror(id, SEQ_TAG, t, plain);
                return Ok(false);
            }
        };
        let mut items = Vec::new();
        for &c in &self.tree.node(id).content {
            let mut e = Value::zero(elem);
            if self.unmarshal(c, elem, &mut e)? {
                items.push(e);
            }
        }
        *out = Value::Slice(items);
        Ok(true)
    }

    fn duplicate_keys(&mut self, id: Id) -> bool {
        let n = self.tree.node(id);
        let before = self.terrors.len();
        for i in (0..n.content.len()).step_by(2) {
            let ni = self.tree.node(n.content[i]);
            for j in (i + 2..n.content.len()).step_by(2) {
                let nj = self.tree.node(n.content[j]);
                if ni.kind == nj.kind && ni.value == nj.value {
                    self.terrors.push(format!(
                        "line {}: mapping key {} already defined at line {}",
                        nj.line,
                        go_quote(&nj.value),
                        ni.line
                    ));
                }
            }
        }
        self.terrors.len() > before
    }

    fn is_merge(&self, id: Id) -> bool {
        let n = self.tree.node(id);
        n.kind == Kind::Scalar
            && n.value == "<<"
            && (n.tag.is_empty() || n.tag == "!" || yamlv3::short_tag(&n.tag) == MERGE_TAG)
    }

    fn is_string_map(&self, id: Id) -> bool {
        let n = self.tree.node(id);
        n.content.iter().step_by(2).all(|&k| {
            let t = self.tree.short_tag(k);
            t == STR_TAG || t == MERGE_TAG
        })
    }

    /// The key of a mapping as an `interface{}` holds it, for the merges (`mergedFields`).
    fn merge_key(&mut self, id: Id) -> Result<Option<MergeKey>, Fatal> {
        let any = Type {
            kind: "any".into(),
            name: String::new(),
            go: "interface {}".into(),
            elem: None,
        };
        let mut k = Value::Nil;
        Ok(self.unmarshal(id, &any, &mut k)?.then(|| MergeKey(format!("{k:?}"))))
    }

    fn mapping(&mut self, id: Id, t: &Type, out: &mut Value, plain: bool) -> Result<bool, Fatal> {
        if self.duplicate_keys(id) {
            return Ok(false);
        }
        match t.kind.as_str() {
            "struct" => return self.mapping_struct(id, t, out, plain),
            "map" => {}
            "any" if self.is_string_map(id) => {}
            "any" => return self.general_map(id, out),
            _ => {
                self.terror(id, MAP_TAG, t, plain);
                return Ok(false);
            }
        }
        let any = Type {
            kind: "any".into(),
            name: String::new(),
            go: "interface {}".into(),
            elem: None,
        };
        let elem = if t.kind == "map" { t.elem() } else { &any };
        let key_type = Type {
            kind: "string".into(),
            name: String::new(),
            go: "string".into(),
            elem: None,
        };
        let mut merged = self.merged.take();
        let is_new = !matches!(out, Value::Map(_));
        if is_new {
            *out = Value::Map(BTreeMap::new());
        }
        let n = self.tree.node(id);
        let mut merge_node = None;
        for pair in n.content.chunks(2) {
            let (k, v) = (pair[0], pair[1]);
            if self.is_merge(k) {
                merge_node = Some(v);
                continue;
            }
            let mut key = Value::Str(String::new());
            if !self.unmarshal(k, &key_type, &mut key)? {
                continue;
            }
            let Value::Str(key) = key else {
                unreachable!("a string key")
            };
            if let Some(m) = merged.as_mut()
                && !m.insert(MergeKey(format!("{:?}", Value::Str(key.clone()))))
            {
                continue;
            }
            let mut e = Value::zero(elem);
            let good = self.unmarshal(v, elem, &mut e)?;
            let Value::Map(m) = out else { unreachable!("a map") };
            if good || self.tree.short_tag(v) == NULL_TAG && (is_new || !m.contains_key(&key)) {
                m.insert(key, e);
            }
        }
        self.merged = merged;
        if let Some(m) = merge_node {
            self.merge(id, m, t, out)?;
        }
        Ok(true)
    }

    /// A mapping whose keys are not all strings, in an `interface{}` (`map[interface{}]interface{}`).
    fn general_map(&mut self, id: Id, out: &mut Value) -> Result<bool, Fatal> {
        let any = Type {
            kind: "any".into(),
            name: String::new(),
            go: "interface {}".into(),
            elem: None,
        };
        let mut entries: Vec<(Value, Value)> = Vec::new();
        let n = self.tree.node(id);
        for pair in n.content.chunks(2) {
            if self.is_merge(pair[0]) {
                // a merge into a map of any keys: rare enough to leave its order aside
                continue;
            }
            let mut k = Value::Nil;
            if !self.unmarshal(pair[0], &any, &mut k)? {
                continue;
            }
            if matches!(k, Value::Map(_) | Value::Slice(_) | Value::GeneralMap(_)) {
                return Err(Fatal::new(format!("invalid map key: {k:?}")));
            }
            let mut e = Value::Nil;
            let good = self.unmarshal(pair[1], &any, &mut e)?;
            let exists = entries.iter().any(|(x, _)| *x == k);
            if good || self.tree.short_tag(pair[1]) == NULL_TAG && !exists {
                entries.retain(|(x, _)| *x != k);
                entries.push((k, e));
            }
        }
        *out = Value::GeneralMap(entries);
        Ok(true)
    }

    fn mapping_struct(&mut self, id: Id, t: &Type, out: &mut Value, plain: bool) -> Result<bool, Fatal> {
        let s = SHAPE.strukt(&t.name);
        let key_type = Type {
            kind: "string".into(),
            name: String::new(),
            go: "string".into(),
            elem: None,
        };
        let mut merged = self.merged.take();
        let mut done = vec![false; s.fields.len()];
        let n = self.tree.node(id);
        let mut merge_node = None;
        for pair in n.content.chunks(2) {
            let (k, v) = (pair[0], pair[1]);
            if self.is_merge(k) {
                merge_node = Some(v);
                continue;
            }
            let mut name = Value::Str(String::new());
            if !self.unmarshal(k, &key_type, &mut name)? {
                continue;
            }
            let Value::Str(name) = name else {
                unreachable!("a string key")
            };
            if let Some(m) = merged.as_mut()
                && !m.insert(MergeKey(format!("{:?}", Value::Str(name.clone()))))
            {
                continue;
            }
            let line = self.tree.node(k).line;
            match s.fields.iter().position(|f| f.yaml == name) {
                Some(i) => {
                    if done[i] {
                        self.terrors.push(format!(
                            "line {line}: field {name} already set in type {}",
                            go_name(t, plain)
                        ));
                        continue;
                    }
                    done[i] = true;
                    let Value::Struct(fields) = out else {
                        unreachable!("a struct")
                    };
                    let mut field = std::mem::replace(&mut fields[i], Value::Nil);
                    let r = self.unmarshal(v, &s.fields[i].typ, &mut field);
                    let Value::Struct(fields) = out else {
                        unreachable!("a struct")
                    };
                    fields[i] = field;
                    r?;
                }
                None if self.known_fields => {
                    self.terrors.push(format!(
                        "line {line}: field {name} not found in type {}",
                        go_name(t, plain)
                    ));
                }
                None => {}
            }
        }
        self.merged = merged;
        if let Some(m) = merge_node {
            self.merge_as(id, m, t, out, plain)?;
        }
        Ok(true)
    }

    fn merge(&mut self, parent: Id, merge: Id, t: &Type, out: &mut Value) -> Result<(), Fatal> {
        self.merge_as(parent, merge, t, out, false)
    }

    /// `decoder.merge`: the mappings of a `<<` key decoded into the value, the keys already set left alone.
    fn merge_as(&mut self, parent: Id, merge: Id, t: &Type, out: &mut Value, plain: bool) -> Result<(), Fatal> {
        let outer = self.merged.take();
        let mut keys = match &outer {
            Some(m) => m.clone(),
            None => {
                let mut keys = HashSet::new();
                let content = self.tree.node(parent).content.clone();
                for &k in content.iter().step_by(2) {
                    if let Some(mk) = self.merge_key(k)? {
                        keys.insert(mk);
                    }
                }
                keys
            }
        };
        let want_map = || Fatal::new("map merge requires map or sequence of maps as the value");
        let m = self.tree.node(merge);
        let targets: Vec<Id> = match m.kind {
            Kind::Mapping => vec![merge],
            Kind::Alias => {
                if m.alias.is_some_and(|a| self.tree.node(a).kind != Kind::Mapping) {
                    return Err(want_map());
                }
                vec![merge]
            }
            Kind::Sequence => {
                for &c in &m.content {
                    let n = self.tree.node(c);
                    let ok = match n.kind {
                        Kind::Alias => n.alias.is_none_or(|a| self.tree.node(a).kind == Kind::Mapping),
                        Kind::Mapping => true,
                        _ => false,
                    };
                    if !ok {
                        return Err(want_map());
                    }
                }
                m.content.clone()
            }
            _ => return Err(want_map()),
        };
        for target in targets {
            self.merged = Some(keys);
            self.unmarshal_as(target, t, out, plain)?;
            keys = self.merged.take().unwrap_or_default();
        }
        self.merged = outer;
        Ok(())
    }
}

/// A string as Go's `%#v` (and `strconv.Quote`) writes it.
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c == '\u{7f}' => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// The JSON `json.Marshal` writes of a value of a type (`Err`: its error, an unsupported value or type).
pub fn to_json(t: &Type, v: &Value) -> Result<String, String> {
    let mut out = String::new();
    encode(t, v, &mut out)?;
    Ok(out)
}

/// The JSON of a domain's definition.
pub fn domain_json(v: &Value) -> Result<String, String> {
    let root = Type {
        kind: "struct".into(),
        name: SHAPE.root.clone(),
        go: SHAPE.root.clone(),
        elem: None,
    };
    to_json(&root, v)
}

/// Whether `omitempty` leaves a value out (a pointer only when nil).
fn empty(t: &Type, v: &Value) -> bool {
    if t.kind == "ptr" {
        return *v == Value::Nil;
    }
    match v {
        Value::Str(s) => s.is_empty(),
        Value::Bool(b) => !b,
        Value::Int(i) => *i == 0,
        Value::Uint(u) => *u == 0,
        Value::Float(f) => *f == 0.0,
        Value::Nil => true,
        Value::Slice(s) => s.is_empty(),
        Value::Map(m) => m.is_empty(),
        Value::GeneralMap(m) => m.is_empty(),
        Value::Time(_) | Value::Struct(_) => false,
    }
}

fn encode(t: &Type, v: &Value, out: &mut String) -> Result<(), String> {
    match v {
        Value::Str(s) => out.push_str(&graph_json_string(s)),
        Value::Bool(b) => out.push_str(if *b { "true" } else { "false" }),
        Value::Int(i) => out.push_str(&i.to_string()),
        Value::Uint(u) => out.push_str(&u.to_string()),
        Value::Float(f) => {
            if f.is_nan() {
                return Err("json: unsupported value: NaN".into());
            }
            if f.is_infinite() {
                return Err(format!(
                    "json: unsupported value: {}",
                    if *f > 0.0 { "+Inf" } else { "-Inf" }
                ));
            }
            out.push_str(&json_float(*f));
        }
        Value::Time(ts) => {
            out.push('"');
            out.push_str(&ts.rfc3339_nano());
            out.push('"');
        }
        Value::Nil => out.push_str("null"),
        Value::Slice(items) => {
            let elem = if t.kind == "slice" { Some(t.elem()) } else { None };
            out.push('[');
            for (i, x) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                encode(elem.unwrap_or(t), x, out)?;
            }
            out.push(']');
        }
        Value::Map(m) => {
            let elem = if t.kind == "map" { Some(t.elem()) } else { None };
            out.push('{');
            for (i, (k, x)) in m.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                out.push_str(&graph_json_string(k));
                out.push(':');
                encode(elem.unwrap_or(t), x, out)?;
            }
            out.push('}');
        }
        Value::GeneralMap(_) => return Err("json: unsupported type: map[interface {}]interface {}".into()),
        Value::Struct(fields) => {
            let t = if t.kind == "ptr" { t.elem() } else { t };
            let s = SHAPE.strukt(&t.name);
            out.push('{');
            let mut first = true;
            for (f, x) in s.fields.iter().zip(fields) {
                if f.json.is_empty() || f.json_omit && empty(&f.typ, x) {
                    continue;
                }
                if !first {
                    out.push(',');
                }
                first = false;
                out.push_str(&graph_json_string(&f.json));
                out.push(':');
                encode(&f.typ, x, out)?;
            }
            out.push('}');
        }
    }
    Ok(())
}

/// A string as encoding/json writes it (HTML characters escaped, invalid UTF-8 replaced).
fn graph_json_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => out.push_str(&format!("\\u{:04x}", c as u32)),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// A float as encoding/json writes it (`floatEncoder`: `'f'` but for very small or large magnitudes, `'e'` with the
/// exponent's leading zero dropped).
fn json_float(f: f64) -> String {
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        // 'e' with the shortest digits
        let s = format!("{f:e}");
        let (mant, exp) = s.split_once('e').expect("an exponent");
        let (sign, digits) = exp.strip_prefix('-').map_or(('+', exp), |d| ('-', d));
        let digits = if digits.len() < 2 {
            format!("0{digits}")
        } else {
            digits.to_string()
        };
        let mut out = format!("{mant}e{sign}{digits}");
        // Go: e-07 → e-7
        if out.len() >= 4 {
            let n = out.len();
            let b = out.as_bytes();
            if b[n - 4] == b'e' && b[n - 3] == b'-' && b[n - 2] == b'0' {
                out.remove(n - 2);
            }
        }
        return out;
    }
    // 'f' with the shortest digits that read back
    let s = format!("{f}");
    if s == "-0" { "-0".into() } else { s }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn json(src: &str) -> Result<String, String> {
        let v = parse_domain(src.as_bytes()).map_err(|e| e.0)?;
        domain_json(&v)
    }

    #[test]
    fn reads_and_writes_as_go() {
        assert_eq!(
            json("name: x\nversion: 1.10\nnodeTypes: [A, {name: B, colour: red, attributes: [t]}]\n").unwrap(),
            r#"{"name":"x","version":"1.10","nodeTypes":[{"name":"A"},{"name":"B","attributes":[{"name":"t"}]}],"linkTypes":null}"#
        );
    }

    #[test]
    fn words_the_errors_as_go() {
        assert_eq!(
            json("name: x\ncolour: red\nversion: [1]\n").unwrap_err(),
            "parse domain: yaml: unmarshal errors:\n  line 2: field colour not found in type def.Domain\n  \
             line 3: cannot unmarshal !!seq into string"
        );
        assert_eq!(json("").unwrap_err(), "parse domain: EOF");
        assert_eq!(
            json("name: x\nname: y\n").unwrap_err(),
            "parse domain: yaml: unmarshal errors:\n  line 2: mapping key \"name\" already defined at line 1"
        );
        assert_eq!(
            json("nodeTypes: [[a]]\n").unwrap_err(),
            "parse domain: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!seq into def.plain"
        );
    }

    #[test]
    fn floats_as_go() {
        assert_eq!(json_float(1.0), "1");
        assert_eq!(json_float(0.5), "0.5");
        assert_eq!(json_float(1e21), "1e+21");
        assert_eq!(json_float(1e-7), "1e-7");
        assert_eq!(json_float(123456789.0), "123456789");
    }
}
