package def

import "fmt"

// Importer is a source a domain declares its graph can be loaded from (ADR 0148): a git repository for the code domain,
// a spreadsheet, another tool. The domain names it and declares the parameters a run takes, as attributes; the code
// that reads the source is a service of its own that registers with the import registry (graph/importsvc) as
// implementing it, so a source is added by publishing its declaration and deploying its service.
type Importer struct {
	// Name is unique in the domain (lowercase letters, digits, - and _).
	Name        string `yaml:"name" json:"name"`
	Label       string `yaml:"label,omitempty" json:"label,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Params are the parameters of a run; their types, enums and defaults are checked by the registry before the
	// service is asked.
	Params []Attribute `yaml:"params,omitempty" json:"params,omitempty"`
}

// Importer is the importer of the schema named name.
func (s Schema) Importer(name string) (Importer, bool) {
	for _, im := range s.Importers {
		if im.Name == name {
			return im, true
		}
	}
	return Importer{}, false
}

// checkImporters checks the names of the importers of a schema (their parameters are checked with the attributes,
// checkAttributes).
func (s Schema) checkImporters(prefix string, add func(path, format string, args ...any)) {
	seen := map[string]bool{}
	for i, im := range s.Importers {
		path := fmt.Sprintf(prefix+"importers[%d]", i)
		switch {
		case im.Name == "":
			add(path+".name", "name required")
		case !NameRE.MatchString(im.Name):
			add(path+".name", "invalid importer name %q (lowercase letters, digits, - and _)", im.Name)
		case seen[im.Name]:
			add(path+".name", "duplicate importer %s", im.Name)
		}
		seen[im.Name] = true
	}
}

// Enum is the enum of the schema named name (nil when there is none).
func (s Schema) Enum(name string) *Enum {
	for i := range s.Enums {
		if s.Enums[i].Name == name {
			return &s.Enums[i]
		}
	}
	return nil
}
