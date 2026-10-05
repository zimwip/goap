package def

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/domain"
)

// TypeSet is what a methodology needs of the types in force (pkg/typecat.Catalog, or DomainTypes): the qualified
// node and link types ("alm@Requirement") and the ancestors of each node type.
type TypeSet interface {
	HasNodeType(ref string) bool
	HasLinkType(ref string) bool
	Supertypes() map[string][]string
}

// NodeType is a domain node type.
type NodeType struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Attributes define the properties the nodes of the type carry: their code, label, type, widget,
	// validators... The user interface renders and edits a node from them. Subtypes inherit them
	// and may override one by name.
	Attributes []Attribute `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	// Extends makes the type a subtype: it inherits the attributes and link
	// types of its parent, and conditions on the parent apply to it
	// (x.types contains every supertype, ADR 0009 §6).
	Extends string `yaml:"extends,omitempty" json:"extends,omitempty"`
	// Lifecycle names the state machine of the nodes of the type (one of the
	// domain's lifecycles, ADR 0014). Inherited through extends; empty: the
	// nodes have no state.
	Lifecycle string `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	// Document makes the type a document embedding nodes of other types
	// through outgoing "contains" links.
	Document *domain.DocumentSpec `yaml:"document,omitempty" json:"document,omitempty"`
	// ChangeControlled: nodes are only modified through a change (default
	// true). False: direct writes, and no lifecycle.
	ChangeControlled *bool `yaml:"changeControlled,omitempty" json:"changeControlled,omitempty"`
	// Validators plug node validator instances (ADR 0018) on the type: rules on the node as a whole
	// (across attributes). They run in this order when a node of the type is created or modified,
	// after the validators of its attributes, the supertypes' first.
	Validators []string `yaml:"validators,omitempty" json:"validators,omitempty"`
	// Search declares which properties the node index keeps (ADR 0026): text
	// goes into the full-text and embedding document, facet makes the value
	// filterable and countable. Inherited through extends.
	Search []SearchProperty `yaml:"search,omitempty" json:"search,omitempty"`
	// Editor names the editor the user interface opens the nodes of the type
	// with (agent, action, methodology, ...); the UI falls back to its default
	// node editor when it has no editor of that name. Inherited through
	// extends; empty: the default node editor.
	Editor string `yaml:"editor,omitempty" json:"editor,omitempty"`
	// Structure tags the type as the one building a hierarchy every node version is placed in (ADR 0054):
	// the organisational units owning node versions and changes, or the projects nodes are created in. The graph
	// bootstraps its root and checks every write against it. One type per kind across the domains in force.
	Structure *StructureTag `yaml:"structure,omitempty" json:"structure,omitempty"`
	// Requires lists the links a node of the type must carry (ADR 0065): exactly `count` (default 1) outgoing links
	// of each type, checked when the node is created and after each write. A subtype inherits them, redefining one
	// by link.
	Requires []domain.RequiredLink `yaml:"requires,omitempty" json:"requires,omitempty"`
}

// StructureTag tags a node type as a structure of the graph (ADR 0054, domain.Structure).
type StructureTag struct {
	// Kind is domain.StructureOrganisation or domain.StructureProject.
	Kind string `yaml:"kind" json:"kind"`
	// Parent is the link type (of the same domain) from a child to its parent, from and to the type.
	Parent string `yaml:"parent" json:"parent"`
	// Root is the key of the root node the bootstrap creates.
	Root string `yaml:"root" json:"root"`
	// SelfParent: the root links to itself through Parent (a project root) rather than being rootless.
	SelfParent bool `yaml:"selfParent,omitempty" json:"selfParent,omitempty"`
}

// SearchProperty declares how the node index uses a property of a node type.
type SearchProperty struct {
	Property string `yaml:"property" json:"property"`
	Text     bool   `yaml:"text,omitempty" json:"text,omitempty"`
	Facet    bool   `yaml:"facet,omitempty" json:"facet,omitempty"`
}

// Attribute types.
const (
	AttrString  = "string"
	AttrNumber  = "number"
	AttrBoolean = "boolean"
	AttrDate    = "date"
	AttrEnum    = "enum"
	AttrJSON    = "json"
)

// AttributeTypes lists the attribute types.
var AttributeTypes = []string{AttrString, AttrNumber, AttrBoolean, AttrDate, AttrEnum, AttrJSON}

// Attribute widgets: how the user interface lets a value be edited.
const (
	WidgetText     = "text"
	WidgetTextarea = "textarea"
	WidgetDropdown = "dropdown"
	WidgetCheckbox = "checkbox"
	WidgetDate     = "date"
)

// AttributeWidgets lists the widgets.
var AttributeWidgets = []string{WidgetText, WidgetTextarea, WidgetDropdown, WidgetCheckbox, WidgetDate}

// Attribute defines a property of a node type or a link type: the code that keys the value, and what the user
// interface needs to display and edit it. Its validators are the property validator instances (ADR 0018)
// that check the value when a node is created or modified.
type Attribute struct {
	// Name is the code: the key of the value in the node's properties.
	Name        string `yaml:"name" json:"name"`
	Label       string `yaml:"label,omitempty" json:"label,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Type of the value (AttributeTypes); empty: untyped (any value is accepted, the editor shows a text field).
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// Widget edits the value (AttributeWidgets); empty: the usual one of the type.
	Widget string `yaml:"widget,omitempty" json:"widget,omitempty"`
	// Enum names the enum of the domain whose values an enum attribute takes.
	Enum    string `yaml:"enum,omitempty" json:"enum,omitempty"`
	Default string `yaml:"default,omitempty" json:"default,omitempty"`
	// Section groups the attributes in the editor; Order sorts them within the type.
	Section string `yaml:"section,omitempty" json:"section,omitempty"`
	Order   int    `yaml:"order,omitempty" json:"order,omitempty"`
	Tooltip string `yaml:"tooltip,omitempty" json:"tooltip,omitempty"`
	// AsName makes the attribute the display name of the node.
	AsName bool `yaml:"asName,omitempty" json:"asName,omitempty"`
	// Validators are the property_validator instances plugged on the attribute, in call order.
	Validators []string `yaml:"validators,omitempty" json:"validators,omitempty"`
}

// UnmarshalYAML accepts either a plain name or a full object.
func (a *Attribute) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		a.Name = v.Value
		return nil
	}
	type plain Attribute
	return v.Decode((*plain)(a))
}

// DefaultWidget is the widget used when an attribute names none.
func (a Attribute) DefaultWidget() string {
	if a.Widget != "" {
		return a.Widget
	}
	switch a.Type {
	case AttrEnum:
		return WidgetDropdown
	case AttrBoolean:
		return WidgetCheckbox
	case AttrDate:
		return WidgetDate
	}
	return WidgetText
}

// EnumValue is one value of an enum.
type EnumValue struct {
	Value string `yaml:"value" json:"value"`
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
}

// UnmarshalYAML accepts either a plain value or a full object.
func (e *EnumValue) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		e.Value = v.Value
		return nil
	}
	type plain EnumValue
	return v.Decode((*plain)(e))
}

// Enum is a closed list of values of the domain that enum attributes refer to; the values keep their order.
type Enum struct {
	Name        string      `yaml:"name" json:"name"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Values      []EnumValue `yaml:"values,omitempty" json:"values,omitempty"`
}

// AttributeNames lists the names of attributes.
func AttributeNames(as []Attribute) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

// IsChangeControlled tells whether the nodes of the type are modified through changes only.
func (n NodeType) IsChangeControlled() bool { return n.ChangeControlled == nil || *n.ChangeControlled }

// UnmarshalYAML accepts either a plain name or a full object.
func (n *NodeType) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		n.Name = v.Value
		return nil
	}
	type plain NodeType
	return v.Decode((*plain)(n))
}

// LinkType is a domain link type.
type LinkType struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	From        string `yaml:"from,omitempty" json:"from,omitempty"`
	To          string `yaml:"to,omitempty" json:"to,omitempty"`
	// Attributes define what a link of this type carries. The user interface edits a link from them; their
	// validators are checked by the domain but not yet run on commit (pkg/typecat.CheckLink only checks the
	// endpoints' types). "specializes" declares when/priority this way: the generic Activity-specialization
	// condition (architecture plan "Activity concept") a link of that type would carry once one is created.
	Attributes []Attribute `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	// Compose flags a composition link: the target is a part of the source (an aggregate), so a browser or
	// editor shows the targets as the children of the source. It is the semantics the editors read from the
	// domain instead of knowing the link by name ("defines", "sub_activity").
	Compose bool `yaml:"compose,omitempty" json:"compose,omitempty"`
}

// Issue is a validation problem located by a field path such as
// "conditions[2].expr" or "actions[0].pre.has_impacts".
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	// Activity is the flow path of the process, method or step the issue is about ("<process>/<step>/<sub-step>"),
	// empty when it belongs to no activity: the flow draws the issue on that node.
	Activity string `json:"activity,omitempty"`
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

// Issues is a list of validation problems; it implements error.
type Issues []Issue

func (is Issues) Error() string {
	msgs := make([]string, len(is))
	for i, x := range is {
		msgs[i] = x.String()
	}
	return strings.Join(msgs, "; ")
}

var lifecycleNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var NameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
