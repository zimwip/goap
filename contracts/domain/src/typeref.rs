//! References to node and link types (ADR 0012).

use std::fmt;

use crate::{Error, error::INVALID_REF};

/// TypeRef is the reference to a node type or a link type (ADR 0012): the namespace of the domain that declares it
/// and its name in that domain. Its text form is "<namespace>@<name>" (alm@Requirement); it is the value of Node.type
/// and of Link.type.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct TypeRef {
    pub namespace: String,
    pub name: String,
}

/// TypeSep separates the namespace and the name of a type reference.
pub const TYPE_SEP: &str = "@";

impl fmt::Display for TypeRef {
    /// String renders the reference ("alm@Requirement"), or the bare name when it has no namespace.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.namespace.is_empty() {
            f.write_str(&self.name)
        } else {
            write!(f, "{}{TYPE_SEP}{}", self.namespace, self.name)
        }
    }
}

impl TypeRef {
    /// IsZero reports an empty reference.
    pub fn is_zero(&self) -> bool {
        self.name.is_empty()
    }

    /// Qualified reports a reference that names its namespace.
    pub fn qualified(&self) -> bool {
        !self.namespace.is_empty() && !self.name.is_empty()
    }
}

/// ParseTypeRef reads "<namespace>@<name>". A bare name gives a reference without namespace (qualified is false),
/// which a domain definition resolves in its own namespace.
pub fn parse_type_ref(s: &str) -> Result<TypeRef, Error> {
    let Some((ns, name)) = s.split_once(TYPE_SEP) else {
        if s.is_empty() {
            return Err(Error::InvalidRef(format!(
                "empty type reference: {INVALID_REF}"
            )));
        }
        return Ok(TypeRef {
            namespace: String::new(),
            name: s.to_string(),
        });
    };
    if ns.is_empty() || name.is_empty() || name.contains(TYPE_SEP) {
        return Err(Error::InvalidRef(format!(
            "type reference {s:?}: want <namespace>@<name>: {INVALID_REF}"
        )));
    }
    Ok(TypeRef {
        namespace: ns.to_string(),
        name: name.to_string(),
    })
}

/// QualifyIn resolves a reference written in a definition of namespace ns: a bare name is a type of ns.
pub fn qualify_in(ns: &str, s: &str) -> Result<TypeRef, Error> {
    let mut r = parse_type_ref(s)?;
    if r.namespace.is_empty() {
        r.namespace = ns.to_string();
    }
    Ok(r)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse() {
        assert_eq!(
            parse_type_ref("alm@Requirement").unwrap().to_string(),
            "alm@Requirement"
        );
        assert!(!parse_type_ref("Requirement").unwrap().qualified());
        let e = parse_type_ref("a@b@c").unwrap_err();
        assert!(e.is_invalid_ref());
        assert_eq!(
            e.to_string(),
            "type reference \"a@b@c\": want <namespace>@<name>: invalid type reference"
        );
        assert_eq!(
            parse_type_ref("").unwrap_err().to_string(),
            "empty type reference: invalid type reference"
        );
        assert_eq!(qualify_in("alm", "Req").unwrap().to_string(), "alm@Req");
    }
}
