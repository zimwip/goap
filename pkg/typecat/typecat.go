// Package typecat is the type catalogue (ADR 0012 §2): the resolved model of every node type and link type of the
// domains in force and of the built-in domains, keyed by their qualified reference ("<namespace>@<name>").
//
// The registry is the reference of the types; the services that judge or use nodes (graph, engine) hold a catalogue
// built from the published domains it serves, and replace it when a domain is published. A catalogue is immutable.
package typecat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync/atomic"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
)

// Type is the resolved model of a node type.
type Type struct {
	Ref         domain.TypeRef
	Description string
	// Attributes are the attributes of the nodes, the inherited ones first; a subtype redefining one by name
	// replaces it in place.
	Attributes []Attribute
	// Ancestors are the supertypes, nearest first.
	Ancestors []domain.TypeRef
	// Lifecycle is the lifecycle of the nodes (own or inherited), its algorithm guards and actions resolved; nil: the
	// nodes have no state.
	Lifecycle *domain.Lifecycle
	// Document lists the qualified types the nodes embed through "contains" links (own or inherited); nil: not a
	// document.
	Document *domain.DocumentSpec
	// ChangeControlled: the nodes are modified through changes only (default true).
	ChangeControlled bool
	// Validators are the attribute validators and node validators in call order, the supertypes' first (each
	// type: its attributes' validators, then its own node validators).
	Validators []algo.Bound
	// Search are the index declarations, the subtype overriding by property.
	Search []domain.SearchProperty
	// Editor is the IDE editor of the nodes (own or inherited); empty: the default node editor.
	Editor string
	// Requires are the links a node must carry (own and inherited, a subtype redefining one by link), the link types
	// qualified.
	Requires []domain.RequiredLink
	// AdminOnly: the nodes are written by platform administrators only (own or inherited, ADR 0068).
	AdminOnly bool
}

// Attribute is a resolved attribute of a node type.
type Attribute struct {
	def.Attribute
	// From is the type that declares it when it is inherited; empty for the type's own.
	From string
	// Values are the values of the enum of an enum attribute.
	Values []def.EnumValue
}

// PropertyNames lists the names of the attributes.
func (x *Type) PropertyNames() []string {
	out := make([]string, 0, len(x.Attributes))
	for _, a := range x.Attributes {
		out = append(out, a.Name)
	}
	return out
}

// Attribute returns the attribute of the given name.
func (x *Type) Attribute(name string) (Attribute, bool) {
	for _, a := range x.Attributes {
		if a.Name == name {
			return a, true
		}
	}
	return Attribute{}, false
}

// Is reports whether the type is t or one of its subtypes.
func (x *Type) Is(t domain.TypeRef) bool {
	return x.Ref == t || slices.Contains(x.Ancestors, t)
}

// LinkType is the resolved model of a link type. A zero end accepts any node type.
type LinkType struct {
	Ref      domain.TypeRef
	From, To domain.TypeRef
	// Compose: the target is a part of the source (def.LinkType.Compose).
	Compose bool
	// Attributes are what a link of the type carries.
	Attributes []Attribute
}

// Catalog is the set of the types in force.
type Catalog struct {
	types   map[domain.TypeRef]*Type
	links   map[domain.TypeRef]*LinkType
	domains map[string]string // namespace -> version
	// structures are the hierarchies tagged by the domains (ADR 0054), by kind.
	structures map[string]domain.Structure
}

// ErrUnknown marks a reference the catalogue does not resolve.
var ErrUnknown = errors.New("unknown type")

// ErrInvalid marks a node or a link that breaks the types of the catalogue.
var ErrInvalid = errors.New("invalid")

// Builtins returns the built-in domains (methodology, organisation, platform).
func Builtins() []*def.Domain { return def.BuiltinDomains() }

// IsBuiltin reports the namespace of a built-in domain (methodology, organisation, platform): frozen, it changes with
// the code.
func IsBuiltin(ns string) bool { return def.IsBuiltinDomain(ns) }

// Builtin is the catalogue of the built-in domains alone: what a service knows before it has loaded the domains.
func Builtin() *Catalog {
	c, err := New()
	if err != nil {
		panic(err)
	}
	return c
}

// New builds the catalogue of the given domains (one per namespace) and of the built-in domains. A bare reference inside
// a domain (extends, link ends, document) is a type of that domain.
func New(ds ...*def.Domain) (*Catalog, error) {
	all := append(slices.Clone(Builtins()), ds...)
	nb := len(Builtins())
	c := &Catalog{types: map[domain.TypeRef]*Type{}, links: map[domain.TypeRef]*LinkType{}, domains: map[string]string{}, structures: map[string]domain.Structure{}}
	decl := map[domain.TypeRef]declared{}
	for i, d := range all {
		if d == nil {
			continue
		}
		if _, dup := c.domains[d.Name]; dup {
			if i >= nb && IsBuiltin(d.Name) {
				return nil, fmt.Errorf("domain %s: the name of a built-in domain: %w", d.Name, ErrInvalid)
			}
			return nil, fmt.Errorf("domain %s given twice: %w", d.Name, ErrInvalid)
		}
		c.domains[d.Name] = d.Version
		for _, n := range d.NodeTypes {
			ref := domain.TypeRef{Namespace: d.Name, Name: n.Name}
			decl[ref] = declared{d: d, t: n}
			if tag := n.Structure; tag != nil {
				if prev, dup := c.structures[tag.Kind]; dup {
					return nil, fmt.Errorf("type %s: structure %s is already tagged by %s (one type per structure): %w", ref, tag.Kind, prev.Type, ErrInvalid)
				}
				parent, err := domain.QualifyIn(d.Name, tag.Parent)
				if err != nil {
					return nil, fmt.Errorf("type %s: structure parent: %w", ref, err)
				}
				c.structures[tag.Kind] = domain.Structure{Kind: tag.Kind, Type: ref.String(), Namespace: d.Name, Parent: parent.String(), Root: tag.Root, SelfParent: tag.SelfParent}
			}
		}
		for _, l := range d.LinkTypes {
			ref := domain.TypeRef{Namespace: d.Name, Name: l.Name}
			lt := &LinkType{Ref: ref, Compose: l.Compose}
			for _, a := range l.Attributes {
				ra := Attribute{Attribute: a}
				for _, e := range d.Enums {
					if a.Enum != "" && e.Name == a.Enum {
						ra.Values = e.Values
					}
				}
				lt.Attributes = append(lt.Attributes, ra)
			}
			var err error
			if l.From != "" {
				if lt.From, err = domain.QualifyIn(d.Name, l.From); err != nil {
					return nil, fmt.Errorf("link type %s: %w", ref, err)
				}
			}
			if l.To != "" {
				if lt.To, err = domain.QualifyIn(d.Name, l.To); err != nil {
					return nil, fmt.Errorf("link type %s: %w", ref, err)
				}
			}
			if _, dup := c.links[ref]; dup {
				// a link type declared for several pairs of ends: it accepts any pair
				c.links[ref] = &LinkType{Ref: ref, Compose: l.Compose || c.links[ref].Compose, Attributes: c.links[ref].Attributes}
				continue
			}
			c.links[ref] = lt
		}
	}
	for ref := range decl {
		t, err := resolve(ref, decl)
		if err != nil {
			return nil, err
		}
		c.types[ref] = t
	}
	for _, lt := range c.links {
		for _, end := range []domain.TypeRef{lt.From, lt.To} {
			if !end.IsZero() && c.types[end] == nil {
				return nil, fmt.Errorf("link type %s: %s: %w", lt.Ref, end, ErrUnknown)
			}
		}
	}
	for _, kind := range domain.StructureKinds {
		st, ok := c.structures[kind]
		if !ok {
			return nil, fmt.Errorf("no node type is tagged structure %s (ADR 0054): %w", kind, ErrInvalid)
		}
		if _, ok := c.links[mustRef(st.Parent)]; !ok {
			return nil, fmt.Errorf("structure %s: parent link type %s: %w", kind, st.Parent, ErrUnknown)
		}
	}
	// the organisation and the projects meet in one namespace (an Assignment links a unit and a project, ADR 0039):
	// the services reading them read one head
	if o, p := c.structures[domain.StructureOrganisation], c.structures[domain.StructureProject]; o.Namespace != p.Namespace {
		return nil, fmt.Errorf("the organisation (%s) and the projects (%s) must live in one namespace: %w", o.Type, p.Type, ErrInvalid)
	}
	return c, nil
}

// Structures returns both structures with the types belonging to each (the tagged type and its subtypes), sorted.
func (c *Catalog) Structures() domain.Structures {
	out := domain.Structures{Organisation: c.structures[domain.StructureOrganisation], Project: c.structures[domain.StructureProject]}
	for ref := range c.types {
		if c.IsA(ref.String(), out.Organisation.Type) {
			out.OrganisationTypes = append(out.OrganisationTypes, ref.String())
		}
		if c.IsA(ref.String(), out.Project.Type) {
			out.ProjectTypes = append(out.ProjectTypes, ref.String())
		}
	}
	slices.Sort(out.OrganisationTypes)
	slices.Sort(out.ProjectTypes)
	return out
}

func mustRef(s string) domain.TypeRef {
	r, _ := domain.ParseTypeRef(s)
	return r
}

// Structure returns the hierarchy tagged kind (domain.StructureOrganisation, domain.StructureProject, ADR 0054).
func (c *Catalog) Structure(kind string) (domain.Structure, bool) {
	s, ok := c.structures[kind]
	return s, ok
}

// IsA reports whether the node type typ is base or one of its subtypes (false when typ is unknown).
func (c *Catalog) IsA(typ, base string) bool {
	t, ok := c.Type(typ)
	if !ok {
		return false
	}
	b, err := domain.ParseTypeRef(base)
	return err == nil && t.Is(b)
}

type declared struct {
	d *def.Domain
	t def.NodeType
}

// resolve builds the model of a type along its extends chain.
func resolve(ref domain.TypeRef, decl map[domain.TypeRef]declared) (*Type, error) {
	var chain []domain.TypeRef // the type, then its ancestors
	for cur := ref; ; {
		if slices.Contains(chain, cur) {
			return nil, fmt.Errorf("type %s: cyclic subtyping through %s: %w", ref, cur, ErrInvalid)
		}
		dc, ok := decl[cur]
		if !ok {
			return nil, fmt.Errorf("type %s extends %s: %w", ref, cur, ErrUnknown)
		}
		chain = append(chain, cur)
		if dc.t.Extends == "" {
			break
		}
		next, err := domain.QualifyIn(cur.Namespace, dc.t.Extends)
		if err != nil {
			return nil, fmt.Errorf("type %s: %w", cur, err)
		}
		cur = next
	}
	own := decl[ref]
	t := &Type{Ref: ref, Description: own.t.Description, Ancestors: slices.Clone(chain[1:]), ChangeControlled: true}
	attrAt := map[string]int{}
	searchAt := map[string]int{}
	lifecycleSet, controlSet := false, false
	// nearest first for what the nearest declaration decides
	for _, cur := range chain {
		dc := decl[cur]
		if !lifecycleSet && dc.t.Lifecycle != "" {
			lifecycleSet = true
			t.Lifecycle = dc.d.BindLifecycle(dc.d.Lifecycle(dc.t.Lifecycle))
			if t.Lifecycle == nil {
				return nil, fmt.Errorf("type %s: lifecycle %s: %w", cur, dc.t.Lifecycle, ErrUnknown)
			}
		}
		if t.Document == nil && dc.t.Document != nil {
			doc := &domain.DocumentSpec{}
			for _, s := range dc.t.Document.Contains {
				r, err := domain.QualifyIn(cur.Namespace, s)
				if err != nil {
					return nil, fmt.Errorf("type %s: document: %w", cur, err)
				}
				doc.Contains = append(doc.Contains, r.String())
			}
			t.Document = doc
		}
		if !controlSet && dc.t.ChangeControlled != nil {
			controlSet = true
			t.ChangeControlled = *dc.t.ChangeControlled
		}
		if t.Editor == "" {
			t.Editor = dc.t.Editor
		}
		if dc.t.AdminOnly {
			t.AdminOnly = true
		}
	}
	// ancestors first for what accumulates
	requireAt := map[string]int{}
	for _, cur := range slices.Backward(chain) {
		dc := decl[cur]
		for _, r := range dc.t.Requires {
			link, err := domain.QualifyIn(cur.Namespace, r.Link)
			if err != nil {
				return nil, fmt.Errorf("type %s: requires: %w", cur, err)
			}
			rl := domain.RequiredLink{Link: link.String(), Count: r.Exactly()}
			if i, ok := requireAt[rl.Link]; ok {
				t.Requires[i] = rl
				continue
			}
			requireAt[rl.Link] = len(t.Requires)
			t.Requires = append(t.Requires, rl)
		}
		for _, a := range dc.t.Attributes {
			ra := Attribute{Attribute: a}
			if cur != ref {
				ra.From = cur.String()
			}
			if a.Enum != "" {
				for _, e := range dc.d.Enums {
					if e.Name == a.Enum {
						ra.Values = e.Values
					}
				}
			}
			if i, ok := attrAt[a.Name]; ok {
				t.Attributes[i] = ra
				continue
			}
			attrAt[a.Name] = len(t.Attributes)
			t.Attributes = append(t.Attributes, ra)
		}
		t.Validators = append(t.Validators, dc.d.OwnBoundValidators(dc.t)...)
		for _, sp := range dc.t.Search {
			s := domain.SearchProperty{Property: sp.Property, Text: sp.Text, Facet: sp.Facet}
			if i, ok := searchAt[sp.Property]; ok {
				t.Search[i] = s
				continue
			}
			searchAt[sp.Property] = len(t.Search)
			t.Search = append(t.Search, s)
		}
	}
	if t.Lifecycle != nil {
		t.ChangeControlled = true
	}
	return t, nil
}

// Type returns the model of a qualified node type ("alm@Requirement").
func (c *Catalog) Type(ref string) (*Type, bool) {
	r, err := domain.ParseTypeRef(ref)
	if err != nil || !r.Qualified() {
		return nil, false
	}
	t, ok := c.types[r]
	return t, ok
}

// LinkType returns the model of a qualified link type ("alm@verifies").
func (c *Catalog) LinkType(ref string) (*LinkType, bool) {
	r, err := domain.ParseTypeRef(ref)
	if err != nil || !r.Qualified() {
		return nil, false
	}
	l, ok := c.links[r]
	return l, ok
}

// Lifecycle is the lifecycle of the nodes of a type (nil: none, or an unknown type).
func (c *Catalog) Lifecycle(typ string) *domain.Lifecycle {
	if t, ok := c.Type(typ); ok {
		return t.Lifecycle
	}
	return nil
}

// Validators are the property validators of a type in call order.
func (c *Catalog) Validators(typ string) []algo.Bound {
	if t, ok := c.Type(typ); ok {
		return t.Validators
	}
	return nil
}

// AttributeChecks are what the graph checks of the properties of the nodes of a type (graph.TypeCatalog).
func (c *Catalog) AttributeChecks(typ string) []domain.AttributeCheck {
	t, ok := c.Type(typ)
	if !ok {
		return nil
	}
	out := make([]domain.AttributeCheck, 0, len(t.Attributes))
	for _, a := range t.Attributes {
		ac := domain.AttributeCheck{Name: a.Name, Type: a.Type, Enum: a.Enum}
		for _, v := range a.Values {
			ac.Values = append(ac.Values, v.Value)
		}
		out = append(out, ac)
	}
	return out
}

// Search are the index declarations of a type.
func (c *Catalog) Search(typ string) []domain.SearchProperty {
	if t, ok := c.Type(typ); ok {
		return t.Search
	}
	return nil
}

// Requires are the links a node of the type must carry (ADR 0065).
func (c *Catalog) Requires(typ string) []domain.RequiredLink {
	if t, ok := c.Type(typ); ok {
		return t.Requires
	}
	return nil
}

// AdminOnly reports a node type whose nodes are written by platform administrators only (ADR 0068).
func (c *Catalog) AdminOnly(typ string) bool {
	t, ok := c.Type(typ)
	return ok && t.AdminOnly
}

// HasNodeType reports a known qualified node type (def.TypeSet).
func (c *Catalog) HasNodeType(ref string) bool { _, ok := c.Type(ref); return ok }

// HasLinkType reports a known qualified link type (def.TypeSet).
func (c *Catalog) HasLinkType(ref string) bool { _, ok := c.LinkType(ref); return ok }

// Types lists the node types, sorted by reference.
func (c *Catalog) Types() []*Type {
	out := make([]*Type, 0, len(c.types))
	for _, t := range c.types {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out
}

// LinkTypes lists the link types, sorted by reference.
func (c *Catalog) LinkTypes() []*LinkType {
	out := make([]*LinkType, 0, len(c.links))
	for _, l := range c.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.String() < out[j].Ref.String() })
	return out
}

// Domains maps the namespace of each domain of the catalogue to its version.
func (c *Catalog) Domains() map[string]string {
	out := make(map[string]string, len(c.domains))
	for k, v := range c.domains {
		out[k] = v
	}
	return out
}

// Supertypes maps each type to its ancestors, nearest first (the `x.types` of the conditions).
func (c *Catalog) Supertypes() map[string][]string {
	out := map[string][]string{}
	for ref, t := range c.types {
		if len(t.Ancestors) == 0 {
			continue
		}
		as := make([]string, len(t.Ancestors))
		for i, a := range t.Ancestors {
			as[i] = a.String()
		}
		out[ref.String()] = as
	}
	return out
}

// CheckNode is the existence rule (ADR 0012 §3): the type of a node of namespace ns resolves and is of ns.
func (c *Catalog) CheckNode(ns, typ string) error {
	t, ok := c.Type(typ)
	if !ok {
		return fmt.Errorf("node type %q: %w", typ, ErrUnknown)
	}
	if t.Ref.Namespace != ns {
		return fmt.Errorf("node type %s belongs to namespace %s, not %s: %w", typ, t.Ref.Namespace, ns, ErrInvalid)
	}
	return nil
}

// CheckLink checks a link of type typ between nodes of types from and to: the link type resolves and its ends
// accept the two nodes (a subtype is accepted where its supertype is).
func (c *Catalog) CheckLink(typ, from, to string) error {
	l, ok := c.LinkType(typ)
	if !ok {
		return fmt.Errorf("link type %q: %w", typ, ErrUnknown)
	}
	for _, e := range []struct {
		want domain.TypeRef
		got  string
		end  string
	}{{l.From, from, "source"}, {l.To, to, "target"}} {
		if e.want.IsZero() {
			continue
		}
		t, ok := c.Type(e.got)
		if !ok {
			return fmt.Errorf("link %s: %s node type %q: %w", typ, e.end, e.got, ErrUnknown)
		}
		if !t.Is(e.want) {
			return fmt.Errorf("link %s: the %s must be a %s, not a %s: %w", typ, e.end, e.want, e.got, ErrInvalid)
		}
	}
	return nil
}

// Source returns the latest published version of every domain (the registry: registrysvc.Service or its client).
type Source func(ctx context.Context) ([]*def.Domain, error)

// Live holds the catalogue in force of a service: the built-in domains until Reload succeeds, then the published
// domains of its source, reloaded when the registry reports a domain event.
type Live struct {
	src Source
	cur atomic.Pointer[Catalog]
}

// NewLive returns a holder of the catalogue of src, starting with the built-in domains.
func NewLive(src Source) *Live {
	l := &Live{src: src}
	l.cur.Store(Builtin())
	return l
}

// Get returns the catalogue in force.
func (l *Live) Get() *Catalog { return l.cur.Load() }

// Reload rebuilds the catalogue from the source; the previous one stays in force on failure.
func (l *Live) Reload(ctx context.Context) error {
	ds, err := l.src(ctx)
	if err != nil {
		return err
	}
	c, err := New(ds...)
	if err != nil {
		return err
	}
	l.cur.Store(c)
	return nil
}
