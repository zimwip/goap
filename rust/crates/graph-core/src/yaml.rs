//! YAML written through `serde_norway` (block style): the values the graph writes (the YAML of a domain, the front
//! matter of an OKF export) built in their order, then serialized. The reader of the text is any YAML parser; the
//! text is not the one Go's `yaml.v3` writes, the content is.
use serde_json::Value as Json;
use serde_norway::{Mapping, Value};

/// A value to write.
#[derive(Debug, Clone, PartialEq)]
pub enum Yaml {
    Str(String),
    /// a number, a boolean, a null or a time: read as a YAML scalar
    Plain(String),
    Seq(Vec<Yaml>),
    /// a mapping, its entries in order
    Map(Vec<(Yaml, Yaml)>),
}

impl Yaml {
    pub fn str(s: impl Into<String>) -> Yaml {
        Yaml::Str(s.into())
    }

    pub fn int(i: i64) -> Yaml {
        Yaml::Plain(i.to_string())
    }

    /// A JSON value, its objects with their keys in order.
    pub fn of_json(v: &Json) -> Yaml {
        match v {
            Json::Null => Yaml::Plain("null".into()),
            Json::Bool(b) => Yaml::Plain(b.to_string()),
            Json::Number(n) => Yaml::Plain(n.to_string()),
            Json::String(s) => Yaml::Str(s.clone()),
            Json::Array(a) => Yaml::Seq(a.iter().map(Yaml::of_json).collect()),
            Json::Object(m) => Yaml::Map(
                m.iter()
                    .map(|(k, v)| (Yaml::Str(k.clone()), Yaml::of_json(v)))
                    .collect(),
            ),
        }
    }

    fn value(&self) -> Value {
        match self {
            Yaml::Str(s) => Value::String(s.clone()),
            Yaml::Plain(s) => serde_norway::from_str(s).unwrap_or_else(|_| Value::String(s.clone())),
            Yaml::Seq(xs) => Value::Sequence(xs.iter().map(Yaml::value).collect()),
            Yaml::Map(es) => Value::Mapping(es.iter().map(|(k, v)| (k.value(), v.value())).collect::<Mapping>()),
        }
    }
}

/// The text of a document.
pub fn to_string(doc: &Yaml) -> String {
    serde_norway::to_string(&doc.value()).unwrap_or_default()
}
