package methodology

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Domain is the object part of the enterprise model: node types and link
// types, versioned on its own and shared by the methodologies that reference
// it (Methodology.DomainRef). Methodologies are the active part (actions,
// agents, conditions, goals) and use domain types as inputs and outputs.
type Domain struct {
	Name        string `yaml:"name" json:"name"`
	Version     string `yaml:"version" json:"version"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Schema      `yaml:",inline"`
}

// ParseDomain decodes a YAML (or JSON) domain. Unknown fields are rejected.
func ParseDomain(data []byte) (*Domain, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Domain
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("parse domain: %w", err)
	}
	return &d, nil
}

// YAML renders the domain.
func (d *Domain) YAML() ([]byte, error) { return yaml.Marshal(d) }

// Validate returns every problem of the domain (empty when valid).
func (d *Domain) Validate() Issues {
	var issues Issues
	add := func(path, format string, args ...any) {
		issues = append(issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	switch {
	case d.Name == "":
		add("name", "name required")
	case !nameRE.MatchString(d.Name):
		add("name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	if len(d.NodeTypes) == 0 {
		add("nodeTypes", "at least one node type required")
	}
	d.Schema.check("", add)
	return issues
}

// SplitRef splits a domain reference "<name>@<version>" (version optional).
func SplitRef(ref string) (name, version string, err error) {
	name, version, _ = strings.Cut(ref, "@")
	if !nameRE.MatchString(name) {
		return "", "", fmt.Errorf("domainRef %q must be <name>[@<version>]", ref)
	}
	return name, version, nil
}

// DomainResolver returns a domain version (empty version: latest published).
type DomainResolver func(name, version string) (*Domain, error)

// Resolve returns the methodology with its Domain filled from DomainRef, so
// that Validate and Compile check it against the shared domain. Without a
// reference it returns m itself. Embedding a domain and referencing one are
// exclusive.
func (m *Methodology) Resolve(resolve DomainResolver) (*Methodology, Issues) {
	if m.DomainRef == "" {
		return m, nil
	}
	if len(m.Domain.NodeTypes) > 0 || len(m.Domain.LinkTypes) > 0 {
		return m, Issues{{Path: "domainRef", Message: "a methodology either references a domain or embeds one"}}
	}
	name, version, err := SplitRef(m.DomainRef)
	if err != nil {
		return m, Issues{{Path: "domainRef", Message: err.Error()}}
	}
	d, err := resolve(name, version)
	if err != nil {
		return m, Issues{{Path: "domainRef", Message: err.Error()}}
	}
	out := *m
	out.Domain = d.Schema
	return &out, nil
}

// Patterns of domain types used inside CEL expressions.
var (
	nodeTypeLiterals = []*regexp.Regexp{
		regexp.MustCompile(`"([^"]+)"\s+in\s+[\w.]+\.types\b`),
		regexp.MustCompile(`\.(?:target|node)\.type\s*==\s*"([^"]+)"`),
	}
	linkTypeLiterals = []*regexp.Regexp{
		regexp.MustCompile(`\.link\.type\s*==\s*"([^"]+)"`),
	}
)

// lintDomainRefs checks the type names a methodology uses in CEL literals and
// builtin params against the referenced domain.
func (m *Methodology) lintDomainRefs(nodeTypes, linkTypes map[string]bool, add func(path, format string, args ...any)) {
	scan := func(path, expr string) {
		for _, re := range nodeTypeLiterals {
			for _, g := range re.FindAllStringSubmatch(expr, -1) {
				if !nodeTypes[g[1]] {
					add(path, "unknown node type %s", g[1])
				}
			}
		}
		for _, re := range linkTypeLiterals {
			for _, g := range re.FindAllStringSubmatch(expr, -1) {
				if !linkTypes[g[1]] {
					add(path, "unknown link type %s", g[1])
				}
			}
		}
	}
	for i, c := range m.Conditions {
		scan(fmt.Sprintf("conditions[%d].expr", i), c.Expr)
	}
	for i, a := range m.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		scan(path+".when", a.When)
		scan(path+".utility", a.Utility)
		if e := a.Expects; e != nil {
			scan(path+".expects.where", e.Where)
		}
		if a.Kind == KindBuiltin {
			if lts, ok := a.Params["linkTypes"].([]any); ok {
				for _, lt := range lts {
					if s, ok := lt.(string); ok && !linkTypes[s] {
						add(path+".params.linkTypes", "unknown link type %s", s)
					}
				}
			}
		}
	}
}

// DomainDir resolves domain references from the YAML files of a directory
// (an empty version picks the last matching file in name order).
func DomainDir(dir string) DomainResolver {
	return func(name, version string) (*Domain, error) {
		files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
		if err != nil {
			return nil, err
		}
		var found *Domain
		for _, f := range files {
			src, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			d, err := ParseDomain(src)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if d.Name == name && (version == "" || d.Version == version) {
				found = d
			}
		}
		if found == nil {
			return nil, fmt.Errorf("domain %s@%s not found in %s", name, version, dir)
		}
		return found, nil
	}
}

// LoadFile parses a methodology file. A domain reference is resolved from the
// "domains" directory next to the methodology's directory (the layout of the
// repository: methodologies/ and domains/).
func LoadFile(path string) (*Methodology, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := Parse(src)
	if err != nil {
		return nil, err
	}
	res, issues := m.Resolve(DomainDir(filepath.Join(filepath.Dir(path), "..", "domains")))
	if len(issues) > 0 {
		return nil, fmt.Errorf("%s: %w", path, issues)
	}
	return res, nil
}
