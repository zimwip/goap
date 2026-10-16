package def

import (
	"fmt"
	"regexp"
)

// ItemType is a type of change item a domain declares (ADR 0128): the items of a change (domain.ChangeItem) name it in
// their `type`. A name is unique across the domains in force (an item belongs to a change, not to a namespace, so its
// type is never qualified). The attributes describe the keys of the data of its items; the graph checks the type and
// enum membership of the keys an item carries, the others stay free. What a type means beyond its data (a validation,
// a permission) is the code of its use case (domain.RegisterItemType, domain.RequireItemPermission).
type ItemType struct {
	Name        string      `yaml:"name" json:"name"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Attributes  []Attribute `yaml:"attributes,omitempty" json:"attributes,omitempty"`
}

// ItemTypeNameRE is the form of the name of an item type: a lower camel case identifier (decisionPoint).
var ItemTypeNameRE = regexp.MustCompile(`^[a-z][A-Za-z0-9_]*$`)

// checkItemTypes checks the names of the item types of a schema (their attributes are checked with the others).
func (s Schema) checkItemTypes(prefix string, add func(path, format string, args ...any)) {
	seen := map[string]bool{}
	for i, t := range s.ItemTypes {
		path := fmt.Sprintf(prefix+"itemTypes[%d].name", i)
		switch {
		case t.Name == "":
			add(path, "name required")
		case !ItemTypeNameRE.MatchString(t.Name):
			add(path, "name must be letters, digits and '_' and start with a lowercase letter")
		case seen[t.Name]:
			add(path, "duplicate item type %s", t.Name)
		}
		seen[t.Name] = true
	}
}
