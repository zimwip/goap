//! The catalogue: the types of the domains in force and of the built-in ones resolved along their `extends` chains
//! (lib/typecat.New), keyed by their qualified reference (`<namespace>@<name>`). Immutable: a new set of domains is a
//! new catalogue.
use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, LazyLock};

use serde_json::{Map, Value};

use crate::algo::{self, Bound};
use crate::def::{self, Domain, EnumValue, Lifecycle};

/// The separator of a qualified type reference.
pub const TYPE_SEP: char = '@';

/// The kinds of structure the platform reads (domain.StructureOrganisation, StructureProject).
pub const STRUCTURE_ORGANISATION: &str = "organisation";
pub const STRUCTURE_PROJECT: &str = "project";

/// A qualified reference with its namespace (`QualifyIn`): a bare name is of the namespace given.
pub fn qualify_in(ns: &str, s: &str) -> Result<String, String> {
    match s.split_once(TYPE_SEP) {
        None if s.is_empty() => Err("empty type reference: invalid type reference".into()),
        None => Ok(format!("{ns}{TYPE_SEP}{s}")),
        Some((n, name)) if n.is_empty() || name.is_empty() || name.contains(TYPE_SEP) => Err(format!(
            "type reference {s:?}: want <namespace>@<name>: invalid type reference"
        )),
        Some(_) => Ok(s.to_string()),
    }
}

/// The namespace of a qualified reference (empty for a bare name).
pub fn namespace_of(r: &str) -> &str {
    r.split_once(TYPE_SEP).map_or("", |(ns, _)| ns)
}

/// A resolved attribute: its definition, the type declaring it when inherited, the values of its enum.
#[derive(Debug, Clone)]
pub struct Attribute {
    pub def: def::Attribute,
    pub from: String,
    pub values: Vec<EnumValue>,
}

impl Attribute {
    fn of(d: &Domain, a: &def::Attribute) -> Attribute {
        Attribute {
            def: a.clone(),
            from: String::new(),
            values: d.enum_values(&a.enumeration).map(<[_]>::to_vec).unwrap_or_default(),
        }
    }
}

/// A resolved node type.
#[derive(Debug, Clone)]
pub struct Type {
    pub reference: String,
    pub description: String,
    /// Inherited first; a subtype redefining one by name replaces it in place.
    pub attributes: Vec<Attribute>,
    /// The supertypes, nearest first.
    pub ancestors: Vec<String>,
    pub lifecycle: Option<Arc<Lifecycle>>,
    pub document: Option<Vec<String>>,
    pub change_controlled: bool,
    /// The names of the validator instances plugged on the type and its attributes, the supertypes' first: a type
    /// with any runs scripts when its nodes are checked.
    pub validators: Vec<String>,
    /// The node validator instances of the type and its supertypes, the supertypes' first.
    pub node_validators: Vec<String>,
    /// The validators resolved in call order (`Type.Validators`): the supertypes' first, and for each type the
    /// validators of its attributes (bound to the attribute) then its node validators.
    pub bound: Vec<Bound>,
    pub search: Vec<def::SearchProperty>,
    pub editor: String,
    pub icon: String,
    pub color: String,
    pub form: Option<Value>,
    /// The link types qualified.
    pub requires: Vec<def::RequiredLink>,
    pub admin_only: bool,
    pub additional_properties: bool,
}

impl Type {
    /// Whether the type is `t` or one of its subtypes.
    pub fn is(&self, t: &str) -> bool {
        self.reference == t || self.ancestors.iter().any(|a| a == t)
    }

    pub fn attribute(&self, name: &str) -> Option<&Attribute> {
        self.attributes.iter().find(|a| a.def.name == name)
    }
}

/// A resolved link type; an empty end accepts any node type.
#[derive(Debug, Clone)]
pub struct LinkType {
    pub reference: String,
    pub from: String,
    pub to: String,
    pub compose: bool,
    pub label: String,
    pub inverse_label: String,
    pub attributes: Vec<Attribute>,
    /// The validators of its attributes, then its own (link validators).
    pub validators: Vec<String>,
    /// The property validators of its attributes resolved, each bound to its attribute.
    pub attribute_validators: Vec<Bound>,
    /// Its link validators resolved.
    pub link_validators: Vec<Bound>,
}

/// A hierarchy a domain tags (domain.Structure, ADR 0054).
#[derive(Debug, Clone)]
pub struct Structure {
    pub kind: String,
    pub typ: String,
    pub namespace: String,
    pub parent: String,
    pub root: String,
    pub self_parent: bool,
    pub bootstrap: Map<String, Value>,
}

/// A type of change item a domain declares (ADR 0128).
#[derive(Debug, Clone)]
pub struct ItemType {
    pub name: String,
    pub description: String,
    pub domain: String,
    pub version: String,
    pub attributes: Vec<Attribute>,
}

/// What the graph checks of a value (domain.AttributeCheck).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AttributeCheck {
    pub name: String,
    pub typ: String,
    pub enumeration: String,
    pub values: Vec<String>,
}

/// The set of the types in force.
#[derive(Debug, Default)]
pub struct Catalog {
    types: HashMap<String, Type>,
    links: HashMap<String, LinkType>,
    domains: BTreeMap<String, String>,
    structures: BTreeMap<String, Structure>,
    items: BTreeMap<String, ItemType>,
    /// The namespaces whose domain runs algorithms (validators, transition guards or actions).
    scripted: Vec<String>,
}

const BUILTIN_SOURCES: [(&str, &str); 3] = [
    (
        "methodology.yaml",
        include_str!("../../../../lib/domains/builtin/methodology.yaml"),
    ),
    (
        "organisation.yaml",
        include_str!("../../../../lib/domains/builtin/organisation.yaml"),
    ),
    (
        "platform.yaml",
        include_str!("../../../../lib/domains/builtin/platform.yaml"),
    ),
];

static BUILTINS: LazyLock<Vec<Domain>> = LazyLock::new(|| {
    BUILTIN_SOURCES
        .iter()
        .map(|(f, src)| Domain::parse(src).unwrap_or_else(|e| panic!("{f}: {e}")))
        .collect()
});

static BUILTIN: LazyLock<Arc<Catalog>> =
    LazyLock::new(|| Arc::new(Catalog::new(&[]).expect("the catalogue of the built-in domains")));

/// The domains that ship with the platform (methodology, organisation, platform).
pub fn builtins() -> &'static [Domain] {
    &BUILTINS
}

/// The YAML of the domains that ship with the platform, in their order.
pub fn builtin_sources() -> impl Iterator<Item = &'static str> {
    BUILTIN_SOURCES.iter().map(|(_, src)| *src)
}

/// Whether a namespace is the one of a built-in domain.
pub fn is_builtin(ns: &str) -> bool {
    BUILTINS.iter().any(|d| d.name == ns)
}

impl Catalog {
    /// The catalogue of the built-in domains alone: what the graph knows before it reads the registry.
    pub fn builtin() -> Arc<Catalog> {
        BUILTIN.clone()
    }

    /// The catalogue of the given domains (one per namespace) and of the built-in ones (typecat.New).
    pub fn new(ds: &[Domain]) -> Result<Catalog, String> {
        let nb = BUILTINS.len();
        let all: Vec<&Domain> = BUILTINS.iter().chain(ds.iter()).collect();
        let mut c = Catalog::default();
        let mut decl: HashMap<String, (&Domain, &def::NodeType)> = HashMap::new();
        for (i, d) in all.iter().enumerate() {
            if c.domains.contains_key(&d.name) {
                if i >= nb && is_builtin(&d.name) {
                    return Err(format!("domain {}: the name of a built-in domain: invalid", d.name));
                }
                return Err(format!("domain {} given twice: invalid", d.name));
            }
            c.domains.insert(d.name.clone(), d.version.clone());
            if d.has_algorithms() {
                c.scripted.push(d.name.clone());
            }
            for it in &d.item_types {
                if let Some(prev) = c.items.get(&it.name) {
                    return Err(format!(
                        "item type {} of {}: already declared by {} (one name across the domains): invalid",
                        it.name, d.name, prev.domain
                    ));
                }
                c.items.insert(
                    it.name.clone(),
                    ItemType {
                        name: it.name.clone(),
                        description: it.description.clone(),
                        domain: d.name.clone(),
                        version: d.version.clone(),
                        attributes: it.attributes.iter().map(|a| Attribute::of(d, a)).collect(),
                    },
                );
            }
            for n in &d.node_types {
                let r = format!("{}{TYPE_SEP}{}", d.name, n.name);
                decl.insert(r.clone(), (d, n));
                if let Some(tag) = &n.structure {
                    if let Some(prev) = c.structures.get(&tag.kind) {
                        return Err(format!(
                            "type {r}: structure {} is already tagged by {} (one type per structure): invalid",
                            tag.kind, prev.typ
                        ));
                    }
                    let parent =
                        qualify_in(&d.name, &tag.parent).map_err(|e| format!("type {r}: structure parent: {e}"))?;
                    c.structures.insert(
                        tag.kind.clone(),
                        Structure {
                            kind: tag.kind.clone(),
                            typ: r.clone(),
                            namespace: d.name.clone(),
                            parent,
                            root: tag.root.clone(),
                            self_parent: tag.self_parent,
                            bootstrap: tag.bootstrap.clone(),
                        },
                    );
                }
            }
            for l in &d.link_types {
                let r = format!("{}{TYPE_SEP}{}", d.name, l.name);
                let (label, inverse_label) = l.labels();
                let mut validators: Vec<String> =
                    l.attributes.iter().flat_map(|a| a.validators.iter().cloned()).collect();
                validators.extend(l.validators.iter().cloned());
                let set = algo::Set::of(&d.algorithms, &d.algorithm_instances);
                let attribute_validators = l
                    .attributes
                    .iter()
                    .flat_map(|a| set.bind_all(&a.validators, algo::PROPERTY_VALIDATOR, &a.name))
                    .collect();
                let link_validators = set.bind_all(&l.validators, algo::LINK_VALIDATOR, "");
                let mut lt = LinkType {
                    reference: r.clone(),
                    from: String::new(),
                    to: String::new(),
                    compose: l.compose,
                    label,
                    inverse_label,
                    attributes: l.attributes.iter().map(|a| Attribute::of(d, a)).collect(),
                    validators,
                    attribute_validators,
                    link_validators,
                };
                if !l.from.is_empty() {
                    lt.from = qualify_in(&d.name, &l.from).map_err(|e| format!("link type {r}: {e}"))?;
                }
                if !l.to.is_empty() {
                    lt.to = qualify_in(&d.name, &l.to).map_err(|e| format!("link type {r}: {e}"))?;
                }
                if let Some(prev) = c.links.get_mut(&r) {
                    // a link type declared for several pairs of ends: it accepts any pair
                    prev.from.clear();
                    prev.to.clear();
                    prev.compose |= l.compose;
                    continue;
                }
                c.links.insert(r, lt);
            }
        }
        for r in decl.keys() {
            let t = resolve(r, &decl)?;
            c.types.insert(r.clone(), t);
        }
        for lt in c.links.values() {
            for end in [&lt.from, &lt.to] {
                if !end.is_empty() && !c.types.contains_key(end) {
                    return Err(format!("link type {}: {end}: unknown type", lt.reference));
                }
            }
        }
        for (kind, st) in &c.structures {
            if !c.links.contains_key(&st.parent) {
                return Err(format!(
                    "structure {kind}: parent link type {}: unknown type",
                    st.parent
                ));
            }
        }
        let (o, p) = (
            c.structures.get(STRUCTURE_ORGANISATION),
            c.structures.get(STRUCTURE_PROJECT),
        );
        if o.map(|s| &s.namespace) != p.map(|s| &s.namespace) {
            return Err(format!(
                "the organisation ({}) and the projects ({}) must live in one namespace: invalid",
                o.map_or("", |s| s.typ.as_str()),
                p.map_or("", |s| s.typ.as_str())
            ));
        }
        Ok(c)
    }

    pub fn typ(&self, r: &str) -> Option<&Type> {
        self.types.get(r)
    }

    /// The item types the domains declare, sorted by name.
    pub fn item_types(&self) -> impl Iterator<Item = &ItemType> {
        self.items.values()
    }

    /// The node types, sorted by reference.
    pub fn types(&self) -> Vec<&Type> {
        let mut out: Vec<&Type> = self.types.values().collect();
        out.sort_by(|a, b| a.reference.cmp(&b.reference));
        out
    }

    /// The link types, sorted by reference.
    pub fn link_types(&self) -> Vec<&LinkType> {
        let mut out: Vec<&LinkType> = self.links.values().collect();
        out.sort_by(|a, b| a.reference.cmp(&b.reference));
        out
    }

    pub fn link_type(&self, r: &str) -> Option<&LinkType> {
        self.links.get(r)
    }

    pub fn structure(&self, kind: &str) -> Option<&Structure> {
        self.structures.get(kind)
    }

    pub fn structures(&self) -> impl Iterator<Item = &Structure> {
        self.structures.values()
    }

    /// Whether the node type `typ` is `base` or one of its subtypes (false when `typ` is unknown).
    pub fn is_a(&self, typ: &str, base: &str) -> bool {
        self.typ(typ).is_some_and(|t| t.is(base))
    }

    /// The validators of a node type in call order (`Catalog.Validators`).
    pub fn bound_validators(&self, typ: &str) -> &[Bound] {
        self.typ(typ).map(|t| t.bound.as_slice()).unwrap_or_default()
    }

    /// The property validators of the attributes of a link type (`LinkAttributeValidators`).
    pub fn link_attribute_validators(&self, typ: &str) -> &[Bound] {
        self.links
            .get(typ)
            .map(|l| l.attribute_validators.as_slice())
            .unwrap_or_default()
    }

    /// The link validators of a link type (`LinkTypeValidators`).
    pub fn link_type_validators(&self, typ: &str) -> &[Bound] {
        self.links
            .get(typ)
            .map(|l| l.link_validators.as_slice())
            .unwrap_or_default()
    }

    pub fn lifecycle(&self, typ: &str) -> Option<&Arc<Lifecycle>> {
        self.typ(typ).and_then(|t| t.lifecycle.as_ref())
    }

    /// The names of the attributes of a node type and whether the type is open (`additionalProperties`, or unknown).
    pub fn attribute_names(&self, typ: &str) -> (Vec<&str>, bool) {
        match self.typ(typ) {
            None => (Vec::new(), true),
            Some(t) => (
                t.attributes.iter().map(|a| a.def.name.as_str()).collect(),
                t.additional_properties,
            ),
        }
    }

    pub fn attribute_checks(&self, typ: &str) -> Vec<AttributeCheck> {
        self.typ(typ).map(|t| checks(&t.attributes)).unwrap_or_default()
    }

    pub fn link_attribute_checks(&self, typ: &str) -> Vec<AttributeCheck> {
        self.link_type(typ).map(|l| checks(&l.attributes)).unwrap_or_default()
    }

    pub fn item_type(&self, name: &str) -> Option<&ItemType> {
        self.items.get(name)
    }

    /// What the graph checks of the data of the items of a type, and whether a domain declares it.
    pub fn item_attribute_checks(&self, name: &str) -> Option<Vec<AttributeCheck>> {
        self.items.get(name).map(|x| checks(&x.attributes))
    }

    pub fn search(&self, typ: &str) -> &[def::SearchProperty] {
        self.typ(typ).map(|t| t.search.as_slice()).unwrap_or_default()
    }

    pub fn requires(&self, typ: &str) -> &[def::RequiredLink] {
        self.typ(typ).map(|t| t.requires.as_slice()).unwrap_or_default()
    }

    pub fn admin_only(&self, typ: &str) -> bool {
        self.typ(typ).is_some_and(|t| t.admin_only)
    }

    pub fn composes(&self, typ: &str) -> bool {
        self.link_type(typ).is_some_and(|l| l.compose)
    }

    /// The namespace of each domain and its version.
    pub fn domains(&self) -> &BTreeMap<String, String> {
        &self.domains
    }

    /// Whether the domain of a namespace runs algorithms: its checks are scripts.
    pub fn scripted(&self, ns: &str) -> bool {
        self.scripted.iter().any(|s| s == ns)
    }

    /// The existence rule (ADR 0012 §3): the type of a node of namespace `ns` resolves and is of `ns`.
    pub fn check_node(&self, ns: &str, typ: &str) -> Result<(), String> {
        let Some(t) = self.typ(typ) else {
            return Err(format!("node type {typ:?}: unknown type"));
        };
        let tns = namespace_of(&t.reference);
        if tns != ns {
            return Err(format!("node type {typ} belongs to namespace {tns}, not {ns}: invalid"));
        }
        Ok(())
    }

    /// A link of type `typ` between nodes of types `from` and `to`: the link type resolves and its ends accept them.
    pub fn check_link(&self, typ: &str, from: &str, to: &str) -> Result<(), String> {
        let Some(l) = self.link_type(typ) else {
            return Err(format!("link type {typ:?}: unknown type"));
        };
        for (want, got, end) in [(&l.from, from, "source"), (&l.to, to, "target")] {
            if want.is_empty() {
                continue;
            }
            let Some(t) = self.typ(got) else {
                return Err(format!("link {typ}: {end} node type {got:?}: unknown type"));
            };
            if !t.is(want) {
                return Err(format!("link {typ}: the {end} must be a {want}, not a {got}: invalid"));
            }
        }
        Ok(())
    }
}

fn checks(as_: &[Attribute]) -> Vec<AttributeCheck> {
    as_.iter()
        .map(|a| AttributeCheck {
            name: a.def.name.clone(),
            typ: a.def.typ.clone(),
            enumeration: a.def.enumeration.clone(),
            values: a.values.iter().map(|v| v.value.clone()).collect(),
        })
        .collect()
}

/// The model of a type along its extends chain (typecat.resolve).
fn resolve(r: &str, decl: &HashMap<String, (&Domain, &def::NodeType)>) -> Result<Type, String> {
    let mut chain: Vec<String> = Vec::new();
    let mut cur = r.to_string();
    loop {
        if chain.contains(&cur) {
            return Err(format!("type {r}: cyclic subtyping through {cur}: invalid"));
        }
        let Some((_, t)) = decl.get(&cur) else {
            return Err(format!("type {r} extends {cur}: unknown type"));
        };
        chain.push(cur.clone());
        if t.extends.is_empty() {
            break;
        }
        cur = qualify_in(namespace_of(&cur), &t.extends).map_err(|e| format!("type {cur}: {e}"))?;
    }
    let (_, own) = decl[r];
    let mut t = Type {
        reference: r.to_string(),
        description: own.description.clone(),
        attributes: Vec::new(),
        ancestors: chain[1..].to_vec(),
        lifecycle: None,
        document: None,
        change_controlled: true,
        validators: Vec::new(),
        node_validators: Vec::new(),
        bound: Vec::new(),
        search: Vec::new(),
        editor: String::new(),
        icon: String::new(),
        color: String::new(),
        form: None,
        requires: Vec::new(),
        admin_only: false,
        additional_properties: false,
    };
    let (mut lifecycle_set, mut control_set) = (false, false);
    // nearest first for what the nearest declaration decides
    for c in &chain {
        let (d, dt) = decl[c];
        if !lifecycle_set && !dt.lifecycle.is_empty() {
            lifecycle_set = true;
            let Some(l) = d.lifecycle(&dt.lifecycle) else {
                return Err(format!("type {c}: lifecycle {}: unknown type", dt.lifecycle));
            };
            t.lifecycle = Some(Arc::new(bind_lifecycle(d, l)));
        }
        if t.document.is_none()
            && let Some(doc) = &dt.document
        {
            let mut contains = Vec::new();
            for s in &doc.contains {
                contains.push(qualify_in(namespace_of(c), s).map_err(|e| format!("type {c}: document: {e}"))?);
            }
            t.document = Some(contains);
        }
        if !control_set && let Some(cc) = dt.change_controlled {
            control_set = true;
            t.change_controlled = cc;
        }
        if t.editor.is_empty() {
            t.editor = dt.editor.clone();
        }
        if t.icon.is_empty() {
            t.icon = dt.icon.clone();
        }
        if t.color.is_empty() {
            t.color = dt.color.clone();
        }
        if t.form.is_none() {
            t.form = dt.form.clone();
        }
        t.admin_only |= dt.admin_only;
        t.additional_properties |= dt.additional_properties;
    }
    // ancestors first for what accumulates
    let mut require_at: HashMap<String, usize> = HashMap::new();
    let mut attr_at: HashMap<String, usize> = HashMap::new();
    let mut search_at: HashMap<String, usize> = HashMap::new();
    for c in chain.iter().rev() {
        let (d, dt) = decl[c];
        for rq in &dt.requires {
            let link = qualify_in(namespace_of(c), &rq.link).map_err(|e| format!("type {c}: requires: {e}"))?;
            let rl = def::RequiredLink {
                link,
                count: rq.exactly(),
            };
            match require_at.get(&rl.link) {
                Some(&i) => t.requires[i] = rl,
                None => {
                    require_at.insert(rl.link.clone(), t.requires.len());
                    t.requires.push(rl);
                }
            }
        }
        for a in &dt.attributes {
            let mut ra = Attribute::of(d, a);
            if c != r {
                ra.from = c.clone();
            }
            match attr_at.get(&a.name) {
                Some(&i) => t.attributes[i] = ra,
                None => {
                    attr_at.insert(a.name.clone(), t.attributes.len());
                    t.attributes.push(ra);
                }
            }
        }
        t.validators
            .extend(dt.attributes.iter().flat_map(|a| a.validators.iter().cloned()));
        t.validators.extend(dt.validators.iter().cloned());
        t.node_validators.extend(dt.validators.iter().cloned());
        let set = algo::Set::of(&d.algorithms, &d.algorithm_instances);
        for a in &dt.attributes {
            t.bound
                .extend(set.bind_all(&a.validators, algo::PROPERTY_VALIDATOR, &a.name));
        }
        t.bound.extend(set.bind_all(&dt.validators, algo::NODE_VALIDATOR, ""));
        for sp in &dt.search {
            match search_at.get(&sp.property) {
                Some(&i) => t.search[i] = sp.clone(),
                None => {
                    search_at.insert(sp.property.clone(), t.search.len());
                    t.search.push(sp.clone());
                }
            }
        }
    }
    if t.lifecycle.is_some() {
        t.change_controlled = true;
    }
    Ok(t)
}

/// A copy of a lifecycle with the guard and action instances of its transitions resolved (`Schema.BindLifecycle`).
fn bind_lifecycle(d: &Domain, l: &Lifecycle) -> Lifecycle {
    let set = algo::Set::of(&d.algorithms, &d.algorithm_instances);
    let mut c = l.clone();
    for t in &mut c.transitions {
        t.guard_algos = set.bind_all(&t.guards, algo::TRANSITION_GUARD, "");
        t.action_algos = set.bind_all(&t.actions, algo::TRANSITION_ACTION, "");
    }
    c
}

#[cfg(test)]
mod tests {
    use super::*;

    const BENCH: &str = "name: bench\nversion: 1.0.0\nnodeTypes:\n  - name: Item\n    attributes:\n      \
        - {name: title, type: string, asName: true}\n      - {name: value, type: number}\n    \
        search: [{property: title, text: true}]\n  - name: T000\n    extends: Item\n    attributes:\n      \
        - {name: a0, type: string}\nlinkTypes:\n  - {name: relates, from: Item, to: Item}\n";

    #[test]
    fn resolves_the_builtin_domains() {
        let c = Catalog::builtin();
        let org = c.structure(STRUCTURE_ORGANISATION).unwrap();
        assert_eq!(
            (org.typ.as_str(), org.root.as_str()),
            ("organisation@OrgUnit", "ORG-DEFAULT")
        );
        assert_eq!(org.parent, "organisation@part_of");
        let proj = c.structure(STRUCTURE_PROJECT).unwrap();
        assert!(proj.self_parent);
        assert!(c.is_a("organisation@User", "organisation@OrgUnit"));
        assert!(c.admin_only("organisation@User"));
        let user = c.typ("organisation@User").unwrap();
        assert_eq!(user.requires[0].link, "organisation@member_of");
        assert!(user.lifecycle.is_some());
        assert!(c.scripted("organisation"));
    }

    #[test]
    fn resolves_a_domain_over_the_builtins() {
        let d = Domain::parse(BENCH).unwrap();
        let c = Catalog::new(&[d]).unwrap();
        let t = c.typ("bench@T000").unwrap();
        assert_eq!(t.ancestors, ["bench@Item"]);
        let (names, open) = c.attribute_names("bench@T000");
        assert_eq!((names, open), (vec!["title", "value", "a0"], false));
        assert_eq!(t.attribute("title").unwrap().from, "bench@Item");
        assert_eq!(c.search("bench@T000")[0].property, "title");
        assert!(c.check_link("bench@relates", "bench@T000", "bench@Item").is_ok());
        assert!(c.check_node("bench", "bench@T000").is_ok());
        assert_eq!(
            c.check_node("other", "bench@T000").unwrap_err(),
            "node type bench@T000 belongs to namespace bench, not other: invalid"
        );
        assert!(!c.scripted("bench"));
        let twice = Catalog::new(&[Domain::parse(BENCH).unwrap(), Domain::parse(BENCH).unwrap()]);
        assert_eq!(twice.unwrap_err(), "domain bench given twice: invalid");
    }
}
