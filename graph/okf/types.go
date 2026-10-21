package okf

import (
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// FromTypes derives the options from the type catalogue: the `asName` attribute names a node, the string attributes
// edited as a text area and the code attributes are the body. A type the catalogue does not know keeps the defaults.
func FromTypes(lookup func(ref string) (*typecat.Type, bool)) Options {
	return Options{
		Name: func(typ string) string {
			if t, ok := lookup(typ); ok {
				for _, a := range t.Attributes {
					if a.AsName {
						return a.Name
					}
				}
			}
			return ""
		},
		Body: func(typ string) []BodyAttribute {
			t, ok := lookup(typ)
			if !ok {
				return []BodyAttribute{{Name: "description", Label: "Description"}}
			}
			var out []BodyAttribute
			for _, a := range t.Attributes {
				code := a.Type == def.AttrCode
				if code || a.Widget == def.WidgetTextarea && (a.Type == "" || a.Type == def.AttrString) {
					label := a.Label
					if label == "" {
						label = a.Name
					}
					out = append(out, BodyAttribute{Name: a.Name, Label: label, Code: code})
				}
			}
			return out
		},
	}
}
