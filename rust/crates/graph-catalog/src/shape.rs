//! The shape of `def.Domain` as Go's encoders see it, generated from the Go structs (`lib/def`
//! `TestRustYAMLSchemaIsCurrent`): each struct's fields in their order, their YAML and JSON names, whether each encoding
//! leaves them out when empty, the Go type of each value, the fields making a value empty for a type implementing
//! yaml.v3's `IsZeroer`, and the field a plain scalar sets for a type whose `UnmarshalYAML` takes a short form.
use std::{collections::HashMap, sync::LazyLock};

use serde::Deserialize;

#[derive(Debug, Deserialize)]
pub struct Shape {
    pub root: String,
    pub types: HashMap<String, Struct>,
}

#[derive(Debug, Deserialize)]
pub struct Struct {
    pub fields: Vec<Field>,
    /// the fields whose emptiness makes the value empty (a type implementing yaml.v3's `IsZeroer`)
    #[serde(default)]
    pub zero: Vec<String>,
    /// the field a plain scalar sets (a type whose `UnmarshalYAML` takes a short form)
    #[serde(default)]
    pub short: String,
}

#[derive(Debug, Deserialize)]
pub struct Field {
    pub yaml: String,
    /// empty for a field JSON leaves out (`json:"-"`)
    pub json: String,
    /// `omitempty` in YAML
    #[serde(default)]
    pub omit: bool,
    /// `omitempty` in JSON
    #[serde(default, rename = "jsonOmit")]
    pub json_omit: bool,
    #[serde(rename = "type")]
    pub typ: Type,
}

#[derive(Debug, Deserialize)]
pub struct Type {
    /// string, bool, int, float, any, slice, map (keyed by strings), ptr or struct
    pub kind: String,
    /// the struct's name, for a struct
    #[serde(default)]
    pub name: String,
    /// the Go type, as `reflect.Type.String` writes it
    #[serde(default)]
    pub go: String,
    pub elem: Option<Box<Type>>,
}

impl Type {
    /// The element of a slice, map or pointer.
    pub fn elem(&self) -> &Type {
        self.elem.as_deref().expect("a slice, map or pointer has an element")
    }
}

impl Shape {
    /// A struct of the shape.
    pub fn strukt(&self, name: &str) -> &Struct {
        &self.types[name]
    }
}

/// The shape of `def.Domain`.
pub static SHAPE: LazyLock<Shape> = LazyLock::new(|| {
    serde_json::from_str(include_str!("domain_shape.gen.json")).expect("the generated shape of def.Domain")
});
