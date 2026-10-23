//! The identifiers of the two sides of a comparison.
//!
//! Each graph service allocates its own UUIDs (nodes, changes, baselines, impacts, links...), so the same call answers
//! different ids on the Go and the Rust side. The proxy pairs them where they appear at the same place of the two
//! answers, then writes the Go ids of the next requests as their Rust twins, and reads the Rust answers back in Go ids
//! before comparing them.
use std::{
    borrow::Cow,
    collections::{BTreeMap, HashMap},
    sync::LazyLock,
};

use regex::Regex;
use serde::Serialize;
use serde_json::Value;

static UUID: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}").unwrap()
});

/// A UUID, or a short id: the first eight digits of one, as the graph names the branch of a change
/// (`change-3013a15b`). The alternation tries the UUID first, so the head of a UUID is never read as a short id.
static TOKEN: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|\b[0-9a-f]{8}\b").unwrap()
});

/// A time (RFC 3339, or as a database writes it): each side stamps its own.
pub static TIME: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$").unwrap());

/// The UUIDs a text holds, in order.
fn uuids(s: &str) -> impl Iterator<Item = &str> {
    UUID.find_iter(s).map(|m| m.as_str())
}

/// Which way a text is translated.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Toward {
    /// Go ids written as the Rust ones: a request sent to the Rust side.
    Rust,
    /// Rust ids written as the Go ones: a Rust answer compared with the Go one.
    Go,
}

/// Two ids paired with others than before: the two sides disagree on what is the same object.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Clash {
    pub go: String,
    pub rust: String,
    /// What the Go id (or the Rust one) was paired with before.
    pub before: String,
}

/// The pairs of ids seen so far, both ways.
#[derive(Debug, Default, Clone, Serialize)]
pub struct IdMap {
    go_to_rust: HashMap<String, String>,
    #[serde(skip)]
    rust_to_go: HashMap<String, String>,
    /// The short ids, both ways (from the UUIDs paired, or seen at the same place).
    #[serde(skip)]
    short_go_to_rust: HashMap<String, String>,
    #[serde(skip)]
    short_rust_to_go: HashMap<String, String>,
}

impl IdMap {
    pub fn len(&self) -> usize {
        self.go_to_rust.len()
    }

    /// The pairs, Go id first, sorted (the file `diff-db` reads).
    pub fn pairs(&self) -> BTreeMap<&str, &str> {
        self.go_to_rust.iter().map(|(g, r)| (g.as_str(), r.as_str())).collect()
    }

    /// Rebuilds a map from its pairs.
    pub fn from_pairs<I: IntoIterator<Item = (String, String)>>(pairs: I) -> Self {
        let mut m = IdMap::default();
        for (g, r) in pairs {
            let _ = m.pair(&g, &r);
        }
        m
    }

    /// Pairs a Go id with a Rust one. The same pair again is nothing; an id already paired with another is a clash
    /// (the pair already held stays).
    pub fn pair(&mut self, go: &str, rust: &str) -> Result<(), Clash> {
        let go = go.to_ascii_lowercase();
        let rust = rust.to_ascii_lowercase();
        if go.len() != rust.len() {
            return Ok(());
        }
        match (self.go_to_rust.get(&go), self.rust_to_go.get(&rust)) {
            (Some(r), _) if *r == rust => Ok(()),
            (Some(r), _) => Err(Clash {
                go,
                rust,
                before: r.clone(),
            }),
            (None, Some(g)) => Err(Clash {
                go,
                rust,
                before: g.clone(),
            }),
            (None, None) => {
                // the short ids of the pair: a clash there is two UUIDs sharing their head, left to the UUIDs
                let _ = self.pair_short(go[..8].to_string(), rust[..8].to_string());
                self.go_to_rust.insert(go.clone(), rust.clone());
                self.rust_to_go.insert(rust, go);
                Ok(())
            }
        }
    }

    fn pair_short(&mut self, go: String, rust: String) -> Result<(), Clash> {
        match (self.short_go_to_rust.get(&go), self.short_rust_to_go.get(&rust)) {
            (Some(r), _) if *r == rust => Ok(()),
            (Some(r), _) => Err(Clash {
                go,
                rust,
                before: r.clone(),
            }),
            (None, Some(g)) => Err(Clash {
                go,
                rust,
                before: g.clone(),
            }),
            (None, None) => {
                self.short_go_to_rust.insert(go.clone(), rust.clone());
                self.short_rust_to_go.insert(rust, go);
                Ok(())
            }
        }
    }

    fn get(&self, id: &str, toward: Toward) -> Option<&String> {
        let id = id.to_ascii_lowercase();
        match (toward, id.len() == 8) {
            (Toward::Rust, false) => self.go_to_rust.get(&id),
            (Toward::Go, false) => self.rust_to_go.get(&id),
            (Toward::Rust, true) => self.short_go_to_rust.get(&id),
            (Toward::Go, true) => self.short_rust_to_go.get(&id),
        }
    }

    /// The text with every paired id written on the other side; an unpaired one stays as it is.
    pub fn text<'a>(&self, s: &'a str, toward: Toward) -> Cow<'a, str> {
        if !TOKEN.is_match(s) {
            return Cow::Borrowed(s);
        }
        TOKEN.replace_all(s, |c: &regex::Captures<'_>| {
            let id = &c[0];
            self.get(id, toward).cloned().unwrap_or_else(|| id.to_string())
        })
    }

    /// The JSON value with every string and every object key translated.
    pub fn json(&self, v: &Value, toward: Toward) -> Value {
        match v {
            Value::String(s) => Value::String(self.text(s, toward).into_owned()),
            Value::Array(a) => Value::Array(a.iter().map(|x| self.json(x, toward)).collect()),
            Value::Object(o) => Value::Object(
                o.iter()
                    .map(|(k, x)| (self.text(k, toward).into_owned(), self.json(x, toward)))
                    .collect(),
            ),
            other => other.clone(),
        }
    }

    /// Pairs the ids of two answers to the same call, walking them side by side: the UUIDs of two strings at the same
    /// place are paired in order when the strings hold as many; the elements of two lists by position; the members
    /// of two objects by name, a name that is an id by its pair, else by the shape of its value. Returns the clashes.
    pub fn pair_json(&mut self, go: &Value, rust: &Value) -> Vec<Clash> {
        let mut clashes = Vec::new();
        self.walk(go, rust, &mut clashes);
        clashes
    }

    /// Pairs the UUIDs of two texts (a header, a message) holding as many. A short id is only ever known from its
    /// UUID: eight digits alone may be anything (`w00-00000012`).
    pub fn pair_text(&mut self, go: &str, rust: &str) -> Vec<Clash> {
        let g: Vec<&str> = uuids(go).collect();
        let r: Vec<&str> = uuids(rust).collect();
        let mut clashes = Vec::new();
        if g.len() == r.len() {
            for (a, b) in g.iter().zip(&r) {
                if let Err(c) = self.pair(a, b) {
                    clashes.push(c);
                }
            }
        }
        clashes
    }

    fn walk(&mut self, go: &Value, rust: &Value, clashes: &mut Vec<Clash>) {
        match (go, rust) {
            (Value::String(g), Value::String(r)) => clashes.extend(self.pair_text(g, r)),
            (Value::Array(g), Value::Array(r)) => {
                for (i, j) in self.match_elements(g, r) {
                    self.walk(&g[i], &r[j], clashes);
                }
            }
            (Value::Object(g), Value::Object(r)) => {
                let mut rest_go = Vec::new();
                let mut rest_rust: Vec<&String> = Vec::new();
                for (k, a) in g {
                    if let Some(b) = r.get(k) {
                        self.walk(a, b, clashes);
                    } else {
                        rest_go.push(k);
                    }
                }
                for k in r.keys() {
                    if !g.contains_key(k) {
                        rest_rust.push(k);
                    }
                }
                // names that are ids: the paired ones first, then one left on each side, then by the shape of the value
                let mut unmatched = Vec::new();
                for k in rest_go {
                    let twin = self.get(k, Toward::Rust).cloned();
                    match twin.and_then(|t| rest_rust.iter().position(|x| x.eq_ignore_ascii_case(&t))) {
                        Some(i) => {
                            let rk = rest_rust.remove(i);
                            self.walk(&g[k], &r[rk], clashes);
                        }
                        None => unmatched.push(k),
                    }
                }
                if unmatched.len() == 1 && rest_rust.len() == 1 {
                    let (k, rk) = (unmatched[0], rest_rust[0]);
                    clashes.extend(self.pair_text(k, rk));
                    self.walk(&g[k], &r[rk], clashes);
                    return;
                }
                for k in unmatched {
                    if !UUID.is_match(k) {
                        continue;
                    }
                    let shape = shape_of(&g[k]);
                    if let Some(i) = rest_rust
                        .iter()
                        .position(|rk| UUID.is_match(rk) && shape_of(&r[*rk]) == shape)
                    {
                        let rk = rest_rust.remove(i);
                        clashes.extend(self.pair_text(k, rk));
                        self.walk(&g[k], &r[rk], clashes);
                    }
                }
            }
            _ => {}
        }
    }
}

impl IdMap {
    /// The value as one side sees it once the ids are left out: the paired ids of the Rust side written as the Go
    /// ones, the other UUIDs and the times blanked (eight digits not known as a short id are not one). Two elements of two lists
    /// alike so are the same object.
    pub fn masked(&self, v: &Value, side: Toward) -> Value {
        let toward = match side {
            Toward::Rust => Toward::Go, // a Rust value, read in Go ids
            Toward::Go => Toward::Rust,
        };
        let token = |s: &str| -> String {
            TOKEN
                .replace_all(s, |c: &regex::Captures<'_>| match (side, self.get(&c[0], toward)) {
                    (Toward::Rust, Some(g)) => g.clone(),
                    (Toward::Go, Some(_)) => c[0].to_ascii_lowercase(),
                    (_, None) if c[0].len() == 8 => c[0].to_string(),
                    (_, None) => "<id>".into(),
                })
                .into_owned()
        };
        match v {
            Value::String(s) if TIME.is_match(s) => Value::String("<time>".into()),
            Value::String(s) => Value::String(token(s)),
            Value::Array(a) => Value::Array(a.iter().map(|x| self.masked(x, side)).collect()),
            Value::Object(o) => Value::Object(o.iter().map(|(k, x)| (token(k), self.masked(x, side))).collect()),
            other => other.clone(),
        }
    }

    /// The elements of two lists that are the same object: alike once masked (a list ordered by an id the two sides
    /// draw apart, the links of a node, comes in another order), the others by position.
    fn match_elements(&self, go: &[Value], rust: &[Value]) -> Vec<(usize, usize)> {
        if go.len() != rust.len() || go.len() < 2 || !go.iter().any(|v| TOKEN.is_match(&v.to_string())) {
            return (0..go.len().min(rust.len())).map(|i| (i, i)).collect();
        }
        let mut by_shape: HashMap<String, Vec<usize>> = HashMap::new();
        for (j, v) in rust.iter().enumerate().rev() {
            by_shape
                .entry(self.masked(v, Toward::Rust).to_string())
                .or_default()
                .push(j);
        }
        let mut used = vec![false; rust.len()];
        let mut pairs = Vec::with_capacity(go.len());
        let mut left = Vec::new();
        for (i, v) in go.iter().enumerate() {
            match by_shape
                .get_mut(&self.masked(v, Toward::Go).to_string())
                .and_then(Vec::pop)
            {
                Some(j) => {
                    used[j] = true;
                    pairs.push((i, j));
                }
                None => left.push(i),
            }
        }
        let mut free = (0..rust.len()).filter(|j| !used[*j]);
        for i in left {
            if let Some(j) = free.next() {
                pairs.push((i, j));
            }
        }
        pairs
    }
}

/// A value with every UUID blanked: what two values of the same object share whatever their ids.
fn shape_of(v: &Value) -> Value {
    match v {
        Value::String(s) => Value::String(UUID.replace_all(s, "<id>").into_owned()),
        Value::Array(a) => Value::Array(a.iter().map(shape_of).collect()),
        Value::Object(o) => Value::Object(
            o.iter()
                .map(|(k, x)| (UUID.replace_all(k, "<id>").into_owned(), shape_of(x)))
                .collect(),
        ),
        other => other.clone(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const G1: &str = "11111111-1111-4111-8111-111111111111";
    const G2: &str = "22222222-2222-4222-8222-222222222222";
    const R1: &str = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    const R2: &str = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";

    #[test]
    fn pairs_ids_at_the_same_place_and_translates_both_ways() {
        let mut m = IdMap::default();
        let go = json!({"change": {"id": G1, "parent": format!("{G2}#3")}, "tags": [G1]});
        let rust = json!({"change": {"id": R1, "parent": format!("{R2}#3")}, "tags": [R1]});
        assert!(m.pair_json(&go, &rust).is_empty());
        assert_eq!(m.len(), 2);
        assert_eq!(m.text(&format!("x:{G2}"), Toward::Rust), format!("x:{R2}"));
        assert_eq!(m.json(&rust, Toward::Go), go);
    }

    #[test]
    fn an_id_paired_twice_with_others_is_a_clash() {
        let mut m = IdMap::default();
        m.pair(G1, R1).unwrap();
        let c = m.pair(G1, R2).unwrap_err();
        assert_eq!(c.before, R1);
        assert!(m.pair(G2, R1).is_err());
        assert!(m.pair(G1, R1).is_ok());
    }

    #[test]
    fn map_keys_that_are_ids_pair_by_shape() {
        let mut m = IdMap::default();
        let go = json!({"byId": {G1: {"key": "A"}, G2: {"key": "B"}}});
        let rust = json!({"byId": {R2: {"key": "B"}, R1: {"key": "A"}}});
        assert!(m.pair_json(&go, &rust).is_empty());
        assert_eq!(m.text(G1, Toward::Rust), R1);
        assert_eq!(m.text(G2, Toward::Rust), R2);
    }

    #[test]
    fn a_short_id_follows_its_uuid() {
        let mut m = IdMap::default();
        m.pair(G1, R1).unwrap();
        assert_eq!(m.text("change-11111111", Toward::Rust), "change-aaaaaaaa");
        // the head of a UUID is not a short id of its own
        assert_eq!(m.text(G2, Toward::Rust), G2);
        // eight digits alone are not one
        let mut m = IdMap::default();
        assert!(m.pair_text("w00-00000012", "w00-00000013").is_empty());
        assert_eq!(m.text("w00-00000012", Toward::Rust), "w00-00000012");
    }

    #[test]
    fn lists_in_another_order_pair_by_what_they_hold() {
        let mut m = IdMap::default();
        m.pair(G1, R1).unwrap();
        m.pair(G2, R2).unwrap();
        let (l1, l2, k1, k2) = (
            "33333333-3333-4333-8333-333333333333",
            "44444444-4444-4444-8444-444444444444",
            "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
            "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
        );
        // the links (new ids) of two targets (paired), sorted by their own id on each side
        let go = json!([{"id": l1, "to": G1}, {"id": l2, "to": G2}]);
        let rust = json!([{"id": k2, "to": R2}, {"id": k1, "to": R1}]);
        assert!(m.pair_json(&go, &rust).is_empty());
        assert_eq!(m.text(l1, Toward::Rust), k1);
        assert_eq!(m.text(l2, Toward::Rust), k2);
    }

    #[test]
    fn texts_with_a_different_number_of_ids_pair_nothing() {
        let mut m = IdMap::default();
        assert!(m.pair_text(&format!("{G1} {G2}"), R1).is_empty());
        assert_eq!(m.len(), 0);
    }
}
