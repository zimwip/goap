//! The values a statement takes and returns, one shape for both dialects: an argument converts itself to the type the
//! database inferred for its placeholder (PostgreSQL), a row holds plain values the readers convert by column.
use std::error::Error;

use bytes::BytesMut;
use chrono::{DateTime, NaiveDateTime, Utc};
use serde_json::Value as Json;
use tokio_postgres::types::{IsNull, ToSql, Type, to_sql_checked};

use crate::StoreError;

/// An argument of a statement.
#[derive(Debug, Clone)]
pub enum Arg {
    Null,
    Text(String),
    /// A time: `timestamptz` on PostgreSQL, the fixed-width UTC text of the Go service on SQLite.
    Time(DateTime<Utc>),
    Int(i64),
    Bool(bool),
    /// A list of texts: a `text[]` or `uuid[]` on PostgreSQL (SQLite takes lists as JSON texts).
    Texts(Vec<String>),
    Ints(Vec<i32>),
}

impl Arg {
    /// A text, null when empty (Go's `nullUUID`).
    pub fn or_null(s: &str) -> Arg {
        if s.is_empty() {
            Arg::Null
        } else {
            Arg::Text(s.to_string())
        }
    }
}

impl From<&str> for Arg {
    fn from(s: &str) -> Arg {
        Arg::Text(s.to_string())
    }
}

impl From<String> for Arg {
    fn from(s: String) -> Arg {
        Arg::Text(s)
    }
}

impl From<i64> for Arg {
    fn from(n: i64) -> Arg {
        Arg::Int(n)
    }
}

impl From<i32> for Arg {
    fn from(n: i32) -> Arg {
        Arg::Int(n.into())
    }
}

impl From<bool> for Arg {
    fn from(b: bool) -> Arg {
        Arg::Bool(b)
    }
}

type BoxError = Box<dyn Error + Sync + Send>;

fn uuid(s: &str) -> Result<uuid::Uuid, BoxError> {
    uuid::Uuid::parse_str(s).map_err(|e| format!("invalid input syntax for type uuid: {s:?}: {e}").into())
}

impl ToSql for Arg {
    fn to_sql(&self, ty: &Type, out: &mut BytesMut) -> Result<IsNull, BoxError> {
        match (self, ty) {
            (Arg::Null, _) => Ok(IsNull::Yes),
            (Arg::Time(t), _) => t.to_sql(ty, out),
            (Arg::Text(s), &Type::UUID) => uuid(s)?.to_sql(ty, out),
            (Arg::Text(s), &Type::JSONB | &Type::JSON) => serde_json::from_str::<Json>(s)?.to_sql(ty, out),
            (Arg::Text(s), _) => s.as_str().to_sql(ty, out),
            (Arg::Int(n), &Type::INT2) => i16::try_from(*n)?.to_sql(ty, out),
            (Arg::Int(n), &Type::INT4) => i32::try_from(*n)?.to_sql(ty, out),
            (Arg::Int(n), _) => n.to_sql(ty, out),
            (Arg::Bool(b), _) => b.to_sql(ty, out),
            (Arg::Texts(v), &Type::UUID_ARRAY) => v
                .iter()
                .map(|s| uuid(s))
                .collect::<Result<Vec<_>, _>>()?
                .to_sql(ty, out),
            (Arg::Texts(v), _) => v.iter().map(String::as_str).collect::<Vec<_>>().to_sql(ty, out),
            (Arg::Ints(v), &Type::INT8_ARRAY) => v.iter().map(|&n| i64::from(n)).collect::<Vec<_>>().to_sql(ty, out),
            (Arg::Ints(v), _) => v.to_sql(ty, out),
        }
    }

    fn accepts(_: &Type) -> bool {
        true
    }

    to_sql_checked!();
}

impl rusqlite::ToSql for Arg {
    fn to_sql(&self) -> rusqlite::Result<rusqlite::types::ToSqlOutput<'_>> {
        use rusqlite::types::{ToSqlOutput, Value};
        Ok(match self {
            Arg::Null => ToSqlOutput::Owned(Value::Null),
            Arg::Time(t) => ToSqlOutput::Owned(Value::Text(t.format(SQLITE_TIME).to_string())),
            Arg::Text(s) => ToSqlOutput::from(s.as_str()),
            Arg::Int(n) => ToSqlOutput::from(*n),
            Arg::Bool(b) => ToSqlOutput::from(*b),
            Arg::Texts(v) => ToSqlOutput::Owned(Value::Text(serde_json::to_string(v).unwrap_or_default())),
            Arg::Ints(v) => ToSqlOutput::Owned(Value::Text(serde_json::to_string(v).unwrap_or_default())),
        })
    }
}

/// A value of a row.
#[derive(Debug, Clone, PartialEq)]
pub enum Val {
    Null,
    Text(String),
    Int(i64),
    Float(f64),
    Bool(bool),
    Json(Json),
    Time(DateTime<Utc>),
    Ints(Vec<i64>),
}

/// A row: its values by column.
#[derive(Debug, Clone, Default)]
pub struct Row(pub Vec<Val>);

/// The fixed-width UTC layout the Go service writes its SQLite times in (text order is time order).
const SQLITE_TIME: &str = "%Y-%m-%dT%H:%M:%S%.9fZ";

impl Row {
    fn get(&self, i: usize) -> &Val {
        self.0.get(i).unwrap_or(&Val::Null)
    }

    pub fn is_null(&self, i: usize) -> bool {
        matches!(self.get(i), Val::Null)
    }

    /// A text column; null reads empty.
    pub fn text(&self, i: usize) -> String {
        match self.get(i) {
            Val::Text(s) => s.clone(),
            Val::Int(n) => n.to_string(),
            Val::Json(j) => j.to_string(),
            _ => String::new(),
        }
    }

    pub fn int(&self, i: usize) -> i64 {
        match self.get(i) {
            Val::Int(n) => *n,
            Val::Float(f) => *f as i64,
            Val::Bool(b) => i64::from(*b),
            Val::Text(s) => s.parse().unwrap_or_default(),
            _ => 0,
        }
    }

    pub fn int32(&self, i: usize) -> i32 {
        self.int(i) as i32
    }

    pub fn bool(&self, i: usize) -> bool {
        match self.get(i) {
            Val::Bool(b) => *b,
            Val::Int(n) => *n != 0,
            _ => false,
        }
    }

    /// A JSON column (jsonb, or the text SQLite keeps it as); null or unreadable reads null.
    pub fn json(&self, i: usize) -> Json {
        match self.get(i) {
            Val::Json(j) => j.clone(),
            Val::Text(s) => serde_json::from_str(s).unwrap_or(Json::Null),
            _ => Json::Null,
        }
    }

    /// A time column: `timestamptz`, or the fixed-width text of SQLite; the zero time reads none.
    pub fn time(&self, i: usize) -> Option<DateTime<Utc>> {
        let t = match self.get(i) {
            Val::Time(t) => Some(*t),
            Val::Text(s) => NaiveDateTime::parse_from_str(s, SQLITE_TIME).ok().map(|t| t.and_utc()),
            _ => None,
        }?;
        // Go's zero time, which the protocol sends as no time
        (t.timestamp() != -62135596800).then_some(t)
    }

    /// An integer list: `int[]`, or the JSON text SQLite keeps it as.
    pub fn ints(&self, i: usize) -> Vec<i64> {
        match self.get(i) {
            Val::Ints(v) => v.clone(),
            Val::Text(s) => serde_json::from_str(s).unwrap_or_default(),
            Val::Json(j) => serde_json::from_value(j.clone()).unwrap_or_default(),
            _ => Vec::new(),
        }
    }

    pub(crate) fn from_pg(r: &tokio_postgres::Row) -> Result<Row, StoreError> {
        let mut out = Vec::with_capacity(r.len());
        for (i, c) in r.columns().iter().enumerate() {
            let v = match *c.type_() {
                Type::TEXT | Type::VARCHAR | Type::NAME | Type::BPCHAR => {
                    r.try_get::<_, Option<String>>(i)?.map_or(Val::Null, Val::Text)
                }
                Type::UUID => r
                    .try_get::<_, Option<uuid::Uuid>>(i)?
                    .map_or(Val::Null, |u| Val::Text(u.to_string())),
                Type::INT2 => r
                    .try_get::<_, Option<i16>>(i)?
                    .map_or(Val::Null, |n| Val::Int(n.into())),
                Type::INT4 => r
                    .try_get::<_, Option<i32>>(i)?
                    .map_or(Val::Null, |n| Val::Int(n.into())),
                Type::INT8 => r.try_get::<_, Option<i64>>(i)?.map_or(Val::Null, Val::Int),
                Type::FLOAT4 => r
                    .try_get::<_, Option<f32>>(i)?
                    .map_or(Val::Null, |n| Val::Float(n.into())),
                Type::FLOAT8 => r.try_get::<_, Option<f64>>(i)?.map_or(Val::Null, Val::Float),
                Type::BOOL => r.try_get::<_, Option<bool>>(i)?.map_or(Val::Null, Val::Bool),
                Type::JSON | Type::JSONB => r.try_get::<_, Option<Json>>(i)?.map_or(Val::Null, Val::Json),
                Type::TIMESTAMPTZ => r.try_get::<_, Option<DateTime<Utc>>>(i)?.map_or(Val::Null, Val::Time),
                Type::TIMESTAMP => r
                    .try_get::<_, Option<NaiveDateTime>>(i)?
                    .map_or(Val::Null, |t| Val::Time(t.and_utc())),
                Type::INT4_ARRAY => r
                    .try_get::<_, Option<Vec<i32>>>(i)?
                    .map_or(Val::Null, |v| Val::Ints(v.into_iter().map(i64::from).collect())),
                Type::INT8_ARRAY => r.try_get::<_, Option<Vec<i64>>>(i)?.map_or(Val::Null, Val::Ints),
                ref t => return Err(StoreError::Other(format!("column {}: type {t} not read", c.name()))),
            };
            out.push(v);
        }
        Ok(Row(out))
    }

    pub(crate) fn from_sqlite(r: &rusqlite::Row<'_>, n: usize) -> rusqlite::Result<Row> {
        use rusqlite::types::ValueRef;
        let mut out = Vec::with_capacity(n);
        for i in 0..n {
            out.push(match r.get_ref(i)? {
                ValueRef::Null => Val::Null,
                ValueRef::Integer(n) => Val::Int(n),
                ValueRef::Real(f) => Val::Float(f),
                ValueRef::Text(t) | ValueRef::Blob(t) => Val::Text(String::from_utf8_lossy(t).into_owned()),
            });
        }
        Ok(Row(out))
    }
}
