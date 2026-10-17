//! The type catalogue (ADR 0012 §2): the resolved model of every node type and link type of the domains in force and
//! of the built-in domains, keyed by their qualified reference ("<namespace>@<name>").

use std::borrow::Borrow;
use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, LazyLock};

use async_trait::async_trait;
use goap_algo::Bound;
use goap_ctx::Ctx;
use goap_def::{Domain, EnumValue, TypeSet};
use goap_domain::{
    AttributeCheck, DocumentSpec, Lifecycle, RequiredLink, STRUCTURE_ORGANISATION,
    STRUCTURE_PROJECT, SearchProperty, Structure, StructureSet, Structures, TypeRef,
    parse_type_ref, qualify_in,
};
use goap_form::Form;

/// The text of `ErrUnknown`: a reference the catalogue does not resolve.
pub const UNKNOWN: &str = "unknown type";
/// The text of `ErrInvalid`: a node or a link that breaks the types of the catalogue.
pub const INVALID: &str = "invalid";

/// The errors of the catalogue. Each variant holds the whole message, as Go's `fmt.Errorf("...: %w", ErrX)` writes it.
#[derive(Debug, Clone, thiserror::Error)]
pub enum Error {
    /// `ErrUnknown`: the message ends with ": unknown type".
    #[error("{0}")]
    Unknown(String),
    /// `ErrInvalid`: the message ends with ": invalid".
    #[error("{0}")]
    Invalid(String),
    /// A malformed type reference of a definition (`goap_domain::Error::InvalidRef`, wrapped).
    #[error("{0}")]
    InvalidRef(String),
    /// The source of a [`Live`] catalogue failed.
    #[error("{0}")]
    Source(Arc<dyn std::error::Error + Send + Sync>),
}

impl Error {
    fn unknown(msg: impl std::fmt::Display) -> Error {
        Error::Unknown(format!("{msg}: {UNKNOWN}"))
    }

    fn invalid(msg: impl std::fmt::Display) -> Error {
        Error::Invalid(format!("{msg}: {INVALID}"))
    }

    fn reference(ctx: impl std::fmt::Display, e: goap_domain::Error) -> Error {
        Error::InvalidRef(format!("{ctx}: {e}"))
    }

    /// `errors.Is(err, ErrUnknown)`.
    pub fn is_unknown(&self) -> bool {
        matches!(self, Error::Unknown(_))
    }

    /// `errors.Is(err, ErrInvalid)`.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Error::Invalid(_))
    }

    /// `errors.Is(err, domain.ErrInvalidRef)`.
    pub fn is_invalid_ref(&self) -> bool {
        matches!(self, Error::InvalidRef(_))
    }
}

/// Type is the resolved model of a node type.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Type {
    pub r#ref: TypeRef,
    pub description: String,
    /// Attributes are the attributes of the nodes, the inherited ones first; a subtype redefining one by name
    /// replaces it in place.
    pub attributes: Vec<Attribute>,
    /// Ancestors are the supertypes, nearest first.
    pub ancestors: Vec<TypeRef>,
    /// Lifecycle is the lifecycle of the nodes (own or inherited), its algorithm guards and actions resolved; None:
    /// the nodes have no state.
    pub lifecycle: Option<Lifecycle>,
    /// Document lists the qualified types the nodes embed through "contains" links (own or inherited); None: not a
    /// document.
    pub document: Option<DocumentSpec>,
    /// ChangeControlled: the nodes are modified through changes only (default true).
    pub change_controlled: bool,
    /// Validators are the attribute validators and node validators in call order, the supertypes' first (each
    /// type: its attributes' validators, then its own node validators).
    pub validators: Vec<Bound>,
    /// Search are the index declarations, the subtype overriding by property.
    pub search: Vec<SearchProperty>,
    /// Editor is the IDE editor of the nodes (own or inherited); empty: the default node editor.
    pub editor: String,
    /// Form is the creation form of the nodes (own or the nearest ancestor's, ADR 0115 §7); None: none.
    pub form: Option<Form>,
    /// Requires are the links a node must carry (own and inherited, a subtype redefining one by link), the link types
    /// qualified.
    pub requires: Vec<RequiredLink>,
    /// AdminOnly: the nodes are written by platform administrators only (own or inherited, ADR 0068).
    pub admin_only: bool,
    /// AdditionalProperties: the nodes may carry properties that are no attribute of the type (own or inherited,
    /// strict attributes opt-out).
    pub additional_properties: bool,
}

/// Attribute is a resolved attribute of a node type (it derefs to its definition).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Attribute {
    pub attribute: goap_def::Attribute,
    /// From is the type that declares it when it is inherited; empty for the type's own.
    pub from: String,
    /// Values are the values of the enum of an enum attribute.
    pub values: Vec<EnumValue>,
}

impl std::ops::Deref for Attribute {
    type Target = goap_def::Attribute;
    fn deref(&self) -> &goap_def::Attribute {
        &self.attribute
    }
}

impl Type {
    /// PropertyNames lists the names of the attributes.
    pub fn property_names(&self) -> Vec<String> {
        self.attributes.iter().map(|a| a.name.clone()).collect()
    }

    /// Attribute returns the attribute of the given name.
    pub fn attribute(&self, name: &str) -> Option<&Attribute> {
        self.attributes.iter().find(|a| a.name == name)
    }

    /// Is reports whether the type is t or one of its subtypes.
    pub fn is(&self, t: &TypeRef) -> bool {
        self.r#ref == *t || self.ancestors.contains(t)
    }
}

/// LinkType is the resolved model of a link type. A zero end accepts any node type.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct LinkType {
    pub r#ref: TypeRef,
    pub from: TypeRef,
    pub to: TypeRef,
    /// Compose: the target is a part of the source (`goap_def::LinkType::compose`).
    pub compose: bool,
    /// Label and InverseLabel read the link from its source and from its target, fallbacks applied
    /// (`goap_def::LinkType::labels`).
    pub label: String,
    pub inverse_label: String,
    /// Attributes are what a link of the type carries.
    pub attributes: Vec<Attribute>,
    /// AttributeValidators are the property validators of the attributes, bound to the attribute, in call order.
    pub attribute_validators: Vec<Bound>,
    /// Validators are the link validators of the type, in call order. A link type has no inheritance.
    pub validators: Vec<Bound>,
}

/// ItemType is the resolved model of a type of change item a domain declares (ADR 0128).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ItemType {
    pub name: String,
    pub description: String,
    /// Domain is the namespace of the domain declaring it, Version its version.
    pub domain: String,
    pub version: String,
    /// Attributes describe the keys of the data of its items, their enums resolved.
    pub attributes: Vec<Attribute>,
}

/// Catalog is the set of the types in force. A catalogue is immutable: share it as an `Arc<Catalog>`.
#[derive(Debug, Clone, Default)]
pub struct Catalog {
    types: HashMap<TypeRef, Type>,
    links: HashMap<TypeRef, LinkType>,
    /// namespace -> version
    domains: BTreeMap<String, String>,
    /// the hierarchies tagged by the domains (ADR 0054), by kind
    structures: BTreeMap<String, Structure>,
    /// the types of change item the domains declare (ADR 0128), by name
    items: BTreeMap<String, ItemType>,
}

/// Builtins returns the built-in domains (methodology, organisation, platform).
pub fn builtins() -> &'static [Domain] {
    goap_def::builtin_domains()
}

/// IsBuiltin reports the namespace of a built-in domain (methodology, organisation, platform): frozen, it changes with
/// the code.
pub fn is_builtin(ns: &str) -> bool {
    goap_def::is_builtin_domain(ns)
}

static BUILTIN: LazyLock<Arc<Catalog>> =
    LazyLock::new(|| Arc::new(Catalog::new::<Domain>(&[]).expect("the built-in catalogue")));

/// Builtin is the catalogue of the built-in domains alone: what a service knows before it has loaded the domains, and
/// what an untyped graph falls back to. A catalogue is immutable, so it is built once.
pub fn builtin() -> Arc<Catalog> {
    BUILTIN.clone()
}

/// New builds the catalogue of the given domains (one per namespace) and of the built-in domains (`Catalog::new`).
pub fn new<D: Borrow<Domain>>(ds: &[D]) -> Result<Catalog, Error> {
    Catalog::new(ds)
}

/// withEnum resolves the values of the enum of an attribute of a domain.
fn with_enum(d: &Domain, a: &goap_def::Attribute) -> Attribute {
    let mut ra = Attribute {
        attribute: a.clone(),
        ..Attribute::default()
    };
    if !a.r#enum.is_empty() {
        for e in &d.enums {
            if e.name == a.r#enum {
                ra.values = e.values.clone();
            }
        }
    }
    ra
}

struct Declared<'a> {
    d: &'a Domain,
    t: &'a goap_def::NodeType,
}

impl Catalog {
    /// New builds the catalogue of the given domains (one per namespace) and of the built-in domains. A bare reference
    /// inside a domain (extends, link ends, document) is a type of that domain.
    pub fn new<D: Borrow<Domain>>(ds: &[D]) -> Result<Catalog, Error> {
        let nb = builtins().len();
        let all: Vec<&Domain> = builtins()
            .iter()
            .chain(ds.iter().map(|d| d.borrow()))
            .collect();
        let mut c = Catalog::default();
        let mut decl: HashMap<TypeRef, Declared<'_>> = HashMap::new();
        for (i, d) in all.iter().enumerate() {
            let d: &Domain = d;
            if c.domains.contains_key(&d.name) {
                if i >= nb && is_builtin(&d.name) {
                    return Err(Error::invalid(format!(
                        "domain {}: the name of a built-in domain",
                        d.name
                    )));
                }
                return Err(Error::invalid(format!("domain {} given twice", d.name)));
            }
            c.domains.insert(d.name.clone(), d.version.clone());
            for it in &d.item_types {
                if let Some(prev) = c.items.get(&it.name) {
                    return Err(Error::invalid(format!(
                        "item type {} of {}: already declared by {} (one name across the domains)",
                        it.name, d.name, prev.domain
                    )));
                }
                let x = ItemType {
                    name: it.name.clone(),
                    description: it.description.clone(),
                    domain: d.name.clone(),
                    version: d.version.clone(),
                    attributes: it.attributes.iter().map(|a| with_enum(d, a)).collect(),
                };
                c.items.insert(it.name.clone(), x);
            }
            for n in &d.node_types {
                let r = TypeRef {
                    namespace: d.name.clone(),
                    name: n.name.clone(),
                };
                decl.insert(r.clone(), Declared { d, t: n });
                if let Some(tag) = &n.structure {
                    if let Some(prev) = c.structures.get(&tag.kind) {
                        return Err(Error::invalid(format!(
                            "type {r}: structure {} is already tagged by {} (one type per structure)",
                            tag.kind, prev.r#type
                        )));
                    }
                    let parent = qualify_in(&d.name, &tag.parent)
                        .map_err(|e| Error::reference(format!("type {r}: structure parent"), e))?;
                    c.structures.insert(
                        tag.kind.clone(),
                        Structure {
                            kind: tag.kind.clone(),
                            r#type: r.to_string(),
                            namespace: d.name.clone(),
                            parent: parent.to_string(),
                            root: tag.root.clone(),
                            self_parent: tag.self_parent,
                            bootstrap: tag.bootstrap.clone(),
                        },
                    );
                }
            }
            for l in &d.link_types {
                let r = TypeRef {
                    namespace: d.name.clone(),
                    name: l.name.clone(),
                };
                let mut lt = LinkType {
                    r#ref: r.clone(),
                    compose: l.compose,
                    ..LinkType::default()
                };
                (lt.label, lt.inverse_label) = l.labels();
                (lt.attribute_validators, lt.validators) = d.own_bound_link_validators(l);
                lt.attributes = l.attributes.iter().map(|a| with_enum(d, a)).collect();
                if !l.from.is_empty() {
                    lt.from = qualify_in(&d.name, &l.from)
                        .map_err(|e| Error::reference(format!("link type {r}"), e))?;
                }
                if !l.to.is_empty() {
                    lt.to = qualify_in(&d.name, &l.to)
                        .map_err(|e| Error::reference(format!("link type {r}"), e))?;
                }
                if let Some(prev) = c.links.get(&r) {
                    // a link type declared for several pairs of ends: it accepts any pair
                    let merged = LinkType {
                        r#ref: r.clone(),
                        compose: l.compose || prev.compose,
                        attributes: prev.attributes.clone(),
                        attribute_validators: prev.attribute_validators.clone(),
                        validators: prev.validators.clone(),
                        label: prev.label.clone(),
                        inverse_label: prev.inverse_label.clone(),
                        ..LinkType::default()
                    };
                    c.links.insert(r, merged);
                    continue;
                }
                c.links.insert(r, lt);
            }
        }
        let mut refs: Vec<&TypeRef> = decl.keys().collect();
        refs.sort();
        for r in refs {
            let t = resolve(r, &decl)?;
            c.types.insert(r.clone(), t);
        }
        let mut links: Vec<&LinkType> = c.links.values().collect();
        links.sort_by(|a, b| a.r#ref.cmp(&b.r#ref));
        for lt in links {
            for end in [&lt.from, &lt.to] {
                if !end.is_zero() && !c.types.contains_key(end) {
                    return Err(Error::unknown(format!("link type {}: {end}", lt.r#ref)));
                }
            }
        }
        for (kind, st) in &c.structures {
            let parent = parse_type_ref(&st.parent).unwrap_or_default();
            if !c.links.contains_key(&parent) {
                return Err(Error::unknown(format!(
                    "structure {kind}: parent link type {}",
                    st.parent
                )));
            }
        }
        // A platform constraint, stated here where the consumers' needs are checked: the organisation and the projects
        // meet in one namespace (an Assignment links a unit and a project, ADR 0039), and the services reading them
        // (goap-access, goap-mcpsvc) read one head. The built-in domain tags both, so they are always declared.
        let o = c
            .structures
            .get(STRUCTURE_ORGANISATION)
            .cloned()
            .unwrap_or_default();
        let p = c
            .structures
            .get(STRUCTURE_PROJECT)
            .cloned()
            .unwrap_or_default();
        if o.namespace != p.namespace {
            return Err(Error::invalid(format!(
                "the organisation ({}) and the projects ({}) must live in one namespace",
                o.r#type, p.r#type
            )));
        }
        Ok(c)
    }

    /// Structures returns every structure the domains declare with the types belonging to each (the tagged type and
    /// its subtypes), by kind, the types sorted.
    pub fn structures(&self) -> Structures {
        let mut out = Vec::new();
        for st in self.structures.values() {
            let mut types: Vec<String> = self
                .types
                .keys()
                .map(|r| r.to_string())
                .filter(|r| self.is_a(r, &st.r#type))
                .collect();
            types.sort();
            out.push(StructureSet {
                structure: st.clone(),
                types,
            });
        }
        Structures(out)
    }

    /// Structure returns the hierarchy tagged kind (`STRUCTURE_ORGANISATION`, `STRUCTURE_PROJECT`, ADR 0054).
    pub fn structure(&self, kind: &str) -> Option<&Structure> {
        self.structures.get(kind)
    }

    /// IsA reports whether the node type typ is base or one of its subtypes (false when typ is unknown).
    pub fn is_a(&self, typ: &str, base: &str) -> bool {
        let Some(t) = self.r#type(typ) else {
            return false;
        };
        parse_type_ref(base).is_ok_and(|b| t.is(&b))
    }

    /// Type returns the model of a qualified node type ("alm@Requirement").
    pub fn r#type(&self, r#ref: &str) -> Option<&Type> {
        let r = parse_type_ref(r#ref).ok()?;
        if !r.qualified() {
            return None;
        }
        self.types.get(&r)
    }

    /// LinkType returns the model of a qualified link type ("alm@verifies").
    pub fn link_type(&self, r#ref: &str) -> Option<&LinkType> {
        let r = parse_type_ref(r#ref).ok()?;
        if !r.qualified() {
            return None;
        }
        self.links.get(&r)
    }

    /// Lifecycle is the lifecycle of the nodes of a type (None: none, or an unknown type).
    pub fn lifecycle(&self, typ: &str) -> Option<&Lifecycle> {
        self.r#type(typ).and_then(|t| t.lifecycle.as_ref())
    }

    /// Validators are the property validators of a type in call order.
    pub fn validators(&self, typ: &str) -> &[Bound] {
        self.r#type(typ).map(|t| &t.validators[..]).unwrap_or(&[])
    }

    /// AttributeChecks are what the graph checks of the properties of the nodes of a type (graph.TypeCatalog).
    pub fn attribute_checks(&self, typ: &str) -> Vec<AttributeCheck> {
        self.r#type(typ)
            .map(|t| attribute_checks(&t.attributes))
            .unwrap_or_default()
    }

    /// AttributeNames are the names of the attributes of a node type, ancestors included, and whether the type is
    /// open: a node of an open type may carry other properties (`additionalProperties: true`, or a type the catalogue
    /// does not know). A node of a closed type carries attributes only (graph.TypeCatalog).
    pub fn attribute_names(&self, typ: &str) -> (Vec<String>, bool) {
        match self.r#type(typ) {
            None => (Vec::new(), true),
            Some(t) => (t.property_names(), t.additional_properties),
        }
    }

    /// LinkAttributeChecks are what the graph checks of the properties of the links of a type (graph.TypeCatalog).
    pub fn link_attribute_checks(&self, typ: &str) -> Vec<AttributeCheck> {
        self.link_type(typ)
            .map(|l| attribute_checks(&l.attributes))
            .unwrap_or_default()
    }

    /// LinkAttributeValidators are the property validators plugged on the attributes of a link type, in call order,
    /// each bound to its attribute (graph.TypeCatalog).
    pub fn link_attribute_validators(&self, typ: &str) -> &[Bound] {
        self.link_type(typ)
            .map(|l| &l.attribute_validators[..])
            .unwrap_or(&[])
    }

    /// LinkTypeValidators are the link validators of a link type, in call order (graph.TypeCatalog).
    pub fn link_type_validators(&self, typ: &str) -> &[Bound] {
        self.link_type(typ)
            .map(|l| &l.validators[..])
            .unwrap_or(&[])
    }

    /// ItemType returns the type of change item of the given name (ADR 0128).
    pub fn item_type(&self, name: &str) -> Option<&ItemType> {
        self.items.get(name)
    }

    /// ItemTypes lists the types of change item the domains declare, by name.
    pub fn item_types(&self) -> Vec<&ItemType> {
        self.items.values().collect()
    }

    /// ItemAttributeChecks are what the graph checks of the data of the items of a type, and whether a domain declares
    /// the type (graph.TypeCatalog): None when no domain declares it.
    pub fn item_attribute_checks(&self, name: &str) -> Option<Vec<AttributeCheck>> {
        self.items
            .get(name)
            .map(|x| attribute_checks(&x.attributes))
    }

    /// Search are the index declarations of a type.
    pub fn search(&self, typ: &str) -> &[SearchProperty] {
        self.r#type(typ).map(|t| &t.search[..]).unwrap_or(&[])
    }

    /// Requires are the links a node of the type must carry (ADR 0065).
    pub fn requires(&self, typ: &str) -> &[RequiredLink] {
        self.r#type(typ).map(|t| &t.requires[..]).unwrap_or(&[])
    }

    /// AdminOnly reports a node type whose nodes are written by platform administrators only (ADR 0068).
    pub fn admin_only(&self, typ: &str) -> bool {
        self.r#type(typ).is_some_and(|t| t.admin_only)
    }

    /// Composes reports a composition link type (`compose: true`): its target is a part of its source (ADR 0077).
    pub fn composes(&self, typ: &str) -> bool {
        self.link_type(typ).is_some_and(|l| l.compose)
    }

    /// HasNodeType reports a known qualified node type (`goap_def::TypeSet`).
    pub fn has_node_type(&self, r#ref: &str) -> bool {
        self.r#type(r#ref).is_some()
    }

    /// HasLinkType reports a known qualified link type (`goap_def::TypeSet`).
    pub fn has_link_type(&self, r#ref: &str) -> bool {
        self.link_type(r#ref).is_some()
    }

    /// Types lists the node types, sorted by reference.
    pub fn types(&self) -> Vec<&Type> {
        let mut out: Vec<&Type> = self.types.values().collect();
        out.sort_by_key(|t| t.r#ref.to_string());
        out
    }

    /// LinkTypes lists the link types, sorted by reference.
    pub fn link_types(&self) -> Vec<&LinkType> {
        let mut out: Vec<&LinkType> = self.links.values().collect();
        out.sort_by_key(|l| l.r#ref.to_string());
        out
    }

    /// Domains maps the namespace of each domain of the catalogue to its version.
    pub fn domains(&self) -> BTreeMap<String, String> {
        self.domains.clone()
    }

    /// Supertypes maps each type to its ancestors, nearest first (the `x.types` of the conditions).
    pub fn supertypes(&self) -> BTreeMap<String, Vec<String>> {
        self.types
            .iter()
            .filter(|(_, t)| !t.ancestors.is_empty())
            .map(|(r, t)| {
                (
                    r.to_string(),
                    t.ancestors.iter().map(|a| a.to_string()).collect(),
                )
            })
            .collect()
    }

    /// CheckNode is the existence rule (ADR 0012 §3): the type of a node of namespace ns resolves and is of ns.
    pub fn check_node(&self, ns: &str, typ: &str) -> Result<(), Error> {
        let Some(t) = self.r#type(typ) else {
            return Err(Error::unknown(format!("node type {typ:?}")));
        };
        if t.r#ref.namespace != ns {
            return Err(Error::invalid(format!(
                "node type {typ} belongs to namespace {}, not {ns}",
                t.r#ref.namespace
            )));
        }
        Ok(())
    }

    /// CheckLink checks a link of type typ between nodes of types from and to: the link type resolves and its ends
    /// accept the two nodes (a subtype is accepted where its supertype is).
    pub fn check_link(&self, typ: &str, from: &str, to: &str) -> Result<(), Error> {
        let Some(l) = self.link_type(typ) else {
            return Err(Error::unknown(format!("link type {typ:?}")));
        };
        for (want, got, end) in [(&l.from, from, "source"), (&l.to, to, "target")] {
            if want.is_zero() {
                continue;
            }
            let Some(t) = self.r#type(got) else {
                return Err(Error::unknown(format!(
                    "link {typ}: {end} node type {got:?}"
                )));
            };
            if !t.is(want) {
                return Err(Error::invalid(format!(
                    "link {typ}: the {end} must be a {want}, not a {got}"
                )));
            }
        }
        Ok(())
    }
}

impl TypeSet for Catalog {
    fn has_node_type(&self, r#ref: &str) -> bool {
        Catalog::has_node_type(self, r#ref)
    }

    fn has_link_type(&self, r#ref: &str) -> bool {
        Catalog::has_link_type(self, r#ref)
    }

    fn supertypes(&self) -> BTreeMap<String, Vec<String>> {
        Catalog::supertypes(self)
    }
}

fn attribute_checks(attrs: &[Attribute]) -> Vec<AttributeCheck> {
    attrs
        .iter()
        .map(|a| AttributeCheck {
            name: a.name.clone(),
            r#type: a.r#type.clone(),
            r#enum: a.r#enum.clone(),
            values: a.values.iter().map(|v| v.value.clone()).collect(),
        })
        .collect()
}

/// resolve builds the model of a type along its extends chain.
fn resolve(r: &TypeRef, decl: &HashMap<TypeRef, Declared<'_>>) -> Result<Type, Error> {
    let mut chain: Vec<TypeRef> = Vec::new(); // the type, then its ancestors
    let mut cur = r.clone();
    loop {
        if chain.contains(&cur) {
            return Err(Error::invalid(format!(
                "type {r}: cyclic subtyping through {cur}"
            )));
        }
        let Some(dc) = decl.get(&cur) else {
            return Err(Error::unknown(format!("type {r} extends {cur}")));
        };
        chain.push(cur.clone());
        if dc.t.extends.is_empty() {
            break;
        }
        let next = qualify_in(&cur.namespace, &dc.t.extends)
            .map_err(|e| Error::reference(format!("type {cur}"), e))?;
        cur = next;
    }
    let own = &decl[r];
    let mut t = Type {
        r#ref: r.clone(),
        description: own.t.description.clone(),
        ancestors: chain[1..].to_vec(),
        change_controlled: true,
        ..Type::default()
    };
    let (mut lifecycle_set, mut control_set) = (false, false);
    // nearest first for what the nearest declaration decides
    for cur in &chain {
        let dc = &decl[cur];
        if !lifecycle_set && !dc.t.lifecycle.is_empty() {
            lifecycle_set = true;
            t.lifecycle = dc.d.bind_lifecycle(dc.d.lifecycle(&dc.t.lifecycle));
            if t.lifecycle.is_none() {
                return Err(Error::unknown(format!(
                    "type {cur}: lifecycle {}",
                    dc.t.lifecycle
                )));
            }
        }
        if t.document.is_none()
            && let Some(d) = &dc.t.document
        {
            let mut doc = DocumentSpec::default();
            for s in &d.contains {
                let rr = qualify_in(&cur.namespace, s)
                    .map_err(|e| Error::reference(format!("type {cur}: document"), e))?;
                doc.contains.push(rr.to_string());
            }
            t.document = Some(doc);
        }
        if !control_set && let Some(cc) = dc.t.change_controlled {
            control_set = true;
            t.change_controlled = cc;
        }
        if t.editor.is_empty() {
            t.editor = dc.t.editor.clone();
        }
        if t.form.is_none() {
            t.form = dc.t.form.clone();
        }
        if dc.t.admin_only {
            t.admin_only = true;
        }
        if dc.t.additional_properties {
            t.additional_properties = true;
        }
    }
    // ancestors first for what accumulates
    let mut attr_at: HashMap<String, usize> = HashMap::new();
    let mut search_at: HashMap<String, usize> = HashMap::new();
    let mut require_at: HashMap<String, usize> = HashMap::new();
    for cur in chain.iter().rev() {
        let dc = &decl[cur];
        for rq in &dc.t.requires {
            let link = qualify_in(&cur.namespace, &rq.link)
                .map_err(|e| Error::reference(format!("type {cur}: requires"), e))?;
            let rl = RequiredLink {
                link: link.to_string(),
                count: rq.exactly(),
            };
            if let Some(&i) = require_at.get(&rl.link) {
                t.requires[i] = rl;
                continue;
            }
            require_at.insert(rl.link.clone(), t.requires.len());
            t.requires.push(rl);
        }
        for a in &dc.t.attributes {
            let mut ra = with_enum(dc.d, a);
            if cur != r {
                ra.from = cur.to_string();
            }
            if let Some(&i) = attr_at.get(&a.name) {
                t.attributes[i] = ra;
                continue;
            }
            attr_at.insert(a.name.clone(), t.attributes.len());
            t.attributes.push(ra);
        }
        t.validators.extend(dc.d.own_bound_validators(dc.t));
        for sp in &dc.t.search {
            let s = SearchProperty {
                property: sp.property.clone(),
                text: sp.text,
                facet: sp.facet,
            };
            if let Some(&i) = search_at.get(&sp.property) {
                t.search[i] = s;
                continue;
            }
            search_at.insert(sp.property.clone(), t.search.len());
            t.search.push(s);
        }
    }
    if t.lifecycle.is_some() {
        t.change_controlled = true;
    }
    Ok(t)
}

/// A boxed error of a [`Source`].
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// Source returns the latest published version of every domain (the registry: registrysvc.Service or its client).
#[async_trait]
pub trait Source: Send + Sync {
    async fn domains(&self, ctx: &Ctx) -> Result<Vec<Domain>, BoxError>;
}

/// Live holds the catalogue in force of a service: the built-in domains until [`Live::reload`] succeeds, then the
/// published domains of its source, reloaded when the registry reports a domain event.
pub struct Live {
    src: Arc<dyn Source>,
    cur: parking_lot::RwLock<Arc<Catalog>>,
}

impl std::fmt::Debug for Live {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Live").finish_non_exhaustive()
    }
}

impl Live {
    /// NewLive returns a holder of the catalogue of src, starting with the built-in domains.
    pub fn new(src: Arc<dyn Source>) -> Live {
        Live {
            src,
            cur: parking_lot::RwLock::new(builtin()),
        }
    }

    /// Get returns the catalogue in force.
    pub fn get(&self) -> Arc<Catalog> {
        self.cur.read().clone()
    }

    /// Reload rebuilds the catalogue from the source; the previous one stays in force on failure.
    pub async fn reload(&self, ctx: &Ctx) -> Result<(), Error> {
        let ds = self
            .src
            .domains(ctx)
            .await
            .map_err(|e| Error::Source(Arc::from(e)))?;
        let c = Catalog::new(&ds)?;
        *self.cur.write() = Arc::new(c);
        Ok(())
    }
}
