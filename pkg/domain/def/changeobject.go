package def

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChangeObjectType is a type of change object (ADR 0098): what can be added to a change beyond its impacts, declared by
// a domain next to its node types and available on every change, as node types are. A change object is a key and a
// value; every write of a key is a new version of the change object, the last one being the change object.
type ChangeObjectType struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Key is the key type: how the key of a change object is made and what is unique.
	Key KeyType `yaml:"key" json:"key"`
	// Scope says where a key is unique: ScopeChange (the default) or ScopeWorkspace (one per workspace of the change).
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	// Attributes define the value of the change objects (ADR 0055), as for nodes.
	Attributes []Attribute `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	// Lifecycle names the state machine of the change objects (one of the domain's lifecycles); empty: no state.
	Lifecycle string `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	// Editor names the editor of the user interface showing the change objects of the type (a tab of the change view);
	// empty: the default object editor.
	Editor string `yaml:"editor,omitempty" json:"editor,omitempty"`
	// Search declares which properties the index keeps (ADR 0026).
	Search []SearchProperty `yaml:"search,omitempty" json:"search,omitempty"`
	// AdditionalProperties: the value may carry properties that are no attribute of the type.
	AdditionalProperties bool `yaml:"additionalProperties,omitempty" json:"additionalProperties,omitempty"`
}

// UnmarshalYAML rejects a bare name: a change object type always declares its key.
func (t *ChangeObjectType) UnmarshalYAML(v *yaml.Node) error {
	type plain ChangeObjectType
	return v.Decode((*plain)(t))
}

// KeyType gives a change object its identity (ADR 0098).
type KeyType struct {
	// Kind is one of KeySingleton, KeySequence, KeyNatural, KeyRef.
	Kind string `yaml:"kind" json:"kind"`
	// Prefix of a sequence key ("RISK" gives RISK-1, RISK-2...).
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	// Attributes whose values, joined, make a natural key.
	Attributes []string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	// Ref is what a ref key designates: one of RefTargets, or a change object type ("<ns>@<Type>", or a bare name of
	// the domain).
	Ref string `yaml:"ref,omitempty" json:"ref,omitempty"`
}

// Key kinds.
const (
	// KeySingleton: one change object of the type per change (per workspace when scoped so).
	KeySingleton = "singleton"
	// KeySequence: allocated by the change at the first write, "<prefix>-<n>", never reused.
	KeySequence = "sequence"
	// KeyNatural: the values of the key attributes, joined.
	KeyNatural = "natural"
	// KeyRef: a reference to another object of the change.
	KeyRef = "ref"
)

// KeyKinds lists the key kinds.
var KeyKinds = []string{KeySingleton, KeySequence, KeyNatural, KeyRef}

// Objects a ref key can designate, besides change object types.
const (
	RefImpact    = "impact"
	RefNode      = "node"
	RefRequest   = "request"
	RefRun       = "run"
	RefWorkspace = "workspace"
)

// RefTargets lists the objects a ref key can designate, besides change object types.
var RefTargets = []string{RefImpact, RefNode, RefRequest, RefRun, RefWorkspace}

// Scopes of a change object type.
const (
	ScopeChange    = "change"
	ScopeWorkspace = "workspace"
)

var keyPrefixRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// ScopeOrDefault is the scope of the type, ScopeChange when it names none.
func (t ChangeObjectType) ScopeOrDefault() string {
	if t.Scope == "" {
		return ScopeChange
	}
	return t.Scope
}

// checkChangeObjectTypes validates the change object types of the schema: unique names (nor a node or link type of
// the schema: a qualified reference names one type), a valid key type, scope, lifecycle, editor, attributes and search.
func (s Schema) checkChangeObjectTypes(prefix string, nodeTypes, linkTypes map[string]bool, add func(path, format string, args ...any)) {
	names := map[string]bool{}
	for _, t := range s.ChangeObjectTypes {
		names[t.Name] = true
	}
	seen := map[string]bool{}
	for i, t := range s.ChangeObjectTypes {
		path := fmt.Sprintf(prefix+"changeObjectTypes[%d]", i)
		switch {
		case t.Name == "":
			add(path+".name", "name required")
		case seen[t.Name]:
			add(path+".name", "duplicate change object type %s", t.Name)
		case nodeTypes[t.Name] || linkTypes[t.Name]:
			add(path+".name", "%s is already a node or link type of the domain", t.Name)
		}
		seen[t.Name] = true
		attrs := map[string]bool{}
		for _, a := range t.Attributes {
			attrs[a.Name] = true
		}
		k := t.Key
		switch k.Kind {
		case "":
			add(path+".key.kind", "the key kind is required (%s)", strings.Join(KeyKinds, ", "))
		case KeySingleton:
		case KeySequence:
			if !keyPrefixRE.MatchString(k.Prefix) {
				add(path+".key.prefix", "a sequence key needs a prefix of uppercase letters, digits and '_' (RISK)")
			}
		case KeyNatural:
			if len(k.Attributes) == 0 {
				add(path+".key.attributes", "a natural key names the attributes it is made of")
			}
			for j, a := range k.Attributes {
				if !attrs[a] {
					add(fmt.Sprintf(path+".key.attributes[%d]", j), "unknown attribute %s of %s", a, t.Name)
				}
			}
		case KeyRef:
			switch {
			case k.Ref == "":
				add(path+".key.ref", "a ref key names what it designates (%s, or a change object type)", strings.Join(RefTargets, ", "))
			case slices.Contains(RefTargets, k.Ref), foreign(k.Ref), names[k.Ref]:
			default:
				add(path+".key.ref", "unknown target %s (%s, or a change object type)", k.Ref, strings.Join(RefTargets, ", "))
			}
		default:
			add(path+".key.kind", "unknown key kind %q (%s)", k.Kind, strings.Join(KeyKinds, ", "))
		}
		if k.Kind != KeySequence && k.Prefix != "" {
			add(path+".key.prefix", "only a sequence key has a prefix")
		}
		if k.Kind != KeyNatural && len(k.Attributes) > 0 {
			add(path+".key.attributes", "only a natural key names attributes")
		}
		if k.Kind != KeyRef && k.Ref != "" {
			add(path+".key.ref", "only a ref key names a target")
		}
		if t.Scope != "" && t.Scope != ScopeChange && t.Scope != ScopeWorkspace {
			add(path+".scope", "unknown scope %q (%s, %s)", t.Scope, ScopeChange, ScopeWorkspace)
		}
		if t.Lifecycle != "" && s.Lifecycle(t.Lifecycle) == nil {
			add(path+".lifecycle", "unknown lifecycle %s", t.Lifecycle)
		}
		if t.Editor != "" && !NameRE.MatchString(t.Editor) {
			add(path+".editor", "invalid editor name %q (lowercase letters, digits, - and _)", t.Editor)
		}
		for j, sp := range t.Search {
			switch {
			case !attrs[sp.Property]:
				add(fmt.Sprintf(path+".search[%d].property", j), "unknown property %q of %s", sp.Property, t.Name)
			case !sp.Text && !sp.Facet:
				add(fmt.Sprintf(path+".search[%d]", j), "property %q is neither text nor facet", sp.Property)
			}
		}
	}
}
