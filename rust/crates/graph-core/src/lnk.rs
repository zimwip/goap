//! Logical URLs (ADR 0154): `lnk:<source>/<type>/<key>[?v=<version>]`, as `contracts/lnk` reads and writes them.
use std::fmt;

/// The scheme of a logical URL.
pub const SCHEME: &str = "lnk";

/// A logical URL: the source, the type and the key of what it names, and optionally an exact version (0: none).
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash)]
pub struct Url {
    pub source: String,
    pub typ: String,
    pub key: String,
    pub version: i64,
}

/// Why a text is not a logical URL.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}: invalid lnk")]
pub struct InvalidUrl(pub String);

impl Url {
    pub fn new(source: &str, typ: &str, key: &str) -> Url {
        Url {
            source: source.into(),
            typ: typ.into(),
            key: key.into(),
            version: 0,
        }
    }

    /// Whether a text names the thing: its key, or a URL of it at any version (Go's `URL.Names`).
    pub fn names(&self, s: &str) -> bool {
        if s.is_empty() || self.is_zero() {
            return false;
        }
        if s == self.key {
            return true;
        }
        Url::parse(s).is_ok_and(|p| p.logical() == self.logical())
    }

    /// The URL without its version: the identity of the thing.
    pub fn logical(&self) -> Url {
        Url {
            version: 0,
            ..self.clone()
        }
    }

    pub fn is_zero(&self) -> bool {
        self.source.is_empty() && self.typ.is_empty() && self.key.is_empty()
    }

    /// Reads `lnk:<source>/<type>/<key>[?v=<version>]`.
    pub fn parse(s: &str) -> Result<Url, InvalidUrl> {
        let bad = |why: &str| InvalidUrl(format!("{s:?}: {why}"));
        let rest = s
            .strip_prefix("lnk:")
            .ok_or_else(|| bad("want lnk:<source>/<type>/<key>"))?;
        let (rest, query) = rest.split_once('?').unwrap_or((rest, ""));
        let (source, rest) = rest
            .split_once('/')
            .ok_or_else(|| bad("want lnk:<source>/<type>/<key>"))?;
        let (typ, key) = rest
            .split_once('/')
            .ok_or_else(|| bad("want lnk:<source>/<type>/<key>"))?;
        let key = path_unescape(key).ok_or_else(|| bad("key"))?;
        let mut u = Url::new(source, typ, &key);
        if !query.is_empty() {
            if query.contains(';') {
                return Err(bad("query"));
            }
            let mut v = None;
            for pair in query.split('&').filter(|p| !p.is_empty()) {
                let (name, value) = pair.split_once('=').unwrap_or((pair, ""));
                let name = query_unescape(name).ok_or_else(|| bad("query"))?;
                let value = query_unescape(value).ok_or_else(|| bad("query"))?;
                if name != "v" {
                    return Err(bad(&format!("unknown parameter {name:?}")));
                }
                v.get_or_insert(value);
            }
            let v = v.unwrap_or_default();
            match v.parse::<i64>() {
                Ok(n) if n >= 1 => u.version = n,
                _ => return Err(bad(&format!("version {v:?}"))),
            }
        }
        u.validate().map_err(|e| InvalidUrl(format!("{s:?}: {}", e.0)))?;
        Ok(u)
    }

    /// The URL of a stored text: the zero URL for an empty or malformed one.
    pub fn of(s: &str) -> Url {
        Url::parse(s).unwrap_or_default()
    }

    pub fn validate(&self) -> Result<(), InvalidUrl> {
        let source_ok = valid_name(
            &self.source,
            |c| c.is_ascii_lowercase(),
            |c| c.is_ascii_lowercase() || c.is_ascii_digit() || matches!(c, '_' | '.' | '-'),
        ) && self.source.len() <= 63;
        let type_ok = valid_name(
            &self.typ,
            |c| c.is_ascii_alphabetic(),
            |c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | '-'),
        ) && self.typ.len() <= 127;
        if !source_ok {
            return Err(InvalidUrl(format!("source {:?}: want [a-z][a-z0-9_.-]*", self.source)));
        }
        if !type_ok {
            return Err(InvalidUrl(format!("type {:?}: want [A-Za-z][A-Za-z0-9_.-]*", self.typ)));
        }
        if self.key.is_empty() {
            return Err(InvalidUrl("empty key".into()));
        }
        if self.version < 0 {
            return Err(InvalidUrl(format!("version {}", self.version)));
        }
        Ok(())
    }
}

fn valid_name(s: &str, first: impl Fn(char) -> bool, rest: impl Fn(char) -> bool) -> bool {
    let mut cs = s.chars();
    cs.next().is_some_and(first) && cs.all(rest)
}

impl fmt::Display for Url {
    /// Renders the URL, the key's slashes kept and every other character a path segment cannot carry escaped (Go's
    /// `url.PathEscape` per segment); the zero URL renders empty.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.is_zero() {
            return Ok(());
        }
        write!(f, "{SCHEME}:{}/{}/", self.source, self.typ)?;
        for (i, part) in self.key.split('/').enumerate() {
            if i > 0 {
                f.write_str("/")?;
            }
            f.write_str(&path_escape(part))?;
        }
        if self.version > 0 {
            write!(f, "?v={}", self.version)?;
        }
        Ok(())
    }
}

/// Go's `url.PathEscape`: everything but the unreserved characters and `$&+:=@` is percent-encoded.
fn path_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~' | b'$' | b'&' | b'+' | b':' | b'=' | b'@')
        {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

fn unhex(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

fn unescape(s: &str, plus_is_space: bool) -> Option<String> {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'%' => {
                let hi = unhex(*bytes.get(i + 1)?)?;
                let lo = unhex(*bytes.get(i + 2)?)?;
                out.push(hi << 4 | lo);
                i += 3;
            }
            b'+' if plus_is_space => {
                out.push(b' ');
                i += 1;
            }
            b => {
                out.push(b);
                i += 1;
            }
        }
    }
    String::from_utf8(out).ok()
}

/// Go's `url.PathUnescape`.
fn path_unescape(s: &str) -> Option<String> {
    unescape(s, false)
}

fn query_unescape(s: &str) -> Option<String> {
    unescape(s, true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips() {
        for s in [
            "lnk:organisation/ProjectUnit/PROJ-ROOT",
            "lnk:code/File/src/main%20x.go?v=3",
            "lnk:alm/Requirement/a$b&c+d:e=f@g",
        ] {
            assert_eq!(Url::parse(s).unwrap().to_string(), s);
        }
        let u = Url::parse("lnk:code/File/a%2Fb").unwrap();
        assert_eq!(u.key, "a/b");
        assert_eq!(u.to_string(), "lnk:code/File/a/b");
    }

    #[test]
    fn refuses_what_go_refuses() {
        for s in [
            "",
            "PROJ-ROOT",
            "lnk:Org/T/k",
            "lnk:org/1T/k",
            "lnk:org/T/",
            "lnk:org/T/k?v=0",
            "lnk:org/T/k?x=1",
            "lnk:org/T/%zz",
        ] {
            assert!(Url::parse(s).is_err(), "{s}");
        }
        assert!(Url::of("nope").is_zero());
    }
}

impl serde::Serialize for Url {
    /// As Go's `MarshalText`: the text of the URL, empty for the zero URL.
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.collect_str(self)
    }
}

impl<'de> serde::Deserialize<'de> for Url {
    /// As Go's `UnmarshalText`: empty is the zero URL, anything else must parse.
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Url, D::Error> {
        let s = String::deserialize(d)?;
        if s.is_empty() {
            return Ok(Url::default());
        }
        Url::parse(&s).map_err(serde::de::Error::custom)
    }
}
