package def

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/pkg/domain"
)

// Domain is the object part of the enterprise model: node types and link
// types, versioned on its own, one per namespace (ADR 0013). Methodologies are the active part (actions,
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
	case !NameRE.MatchString(d.Name):
		add("name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	if len(d.NodeTypes) == 0 && len(d.ChangeObjectTypes) == 0 {
		add("nodeTypes", "at least one node type or change object type required")
	}
	d.Schema.check("", add)
	return issues
}

// DomainTypes is the TypeSet of a list of domains and of the built-in domains: a bare name inside a domain is a
// type of that domain. The registry and the services use the type catalogue (pkg/typecat); this is for files and
// tests.
func DomainTypes(ds ...*Domain) TypeSet {
	t := domainTypes{nodes: map[string]bool{}, links: map[string]bool{}, parents: map[string]string{}}
	for _, d := range append(slices.Clone(BuiltinDomains()), ds...) {
		for _, n := range d.NodeTypes {
			ref := d.Name + domain.TypeSep + n.Name
			t.nodes[ref] = true
			if n.Extends != "" {
				if p, err := domain.QualifyIn(d.Name, n.Extends); err == nil {
					t.parents[ref] = p.String()
				}
			}
		}
		for _, l := range d.LinkTypes {
			t.links[d.Name+domain.TypeSep+l.Name] = true
		}
	}
	return t
}

type domainTypes struct {
	nodes, links map[string]bool
	parents      map[string]string
}

func (t domainTypes) HasNodeType(ref string) bool { return t.nodes[ref] }
func (t domainTypes) HasLinkType(ref string) bool { return t.links[ref] }
func (t domainTypes) Supertypes() map[string][]string {
	out := map[string][]string{}
	for n := range t.nodes {
		seen := map[string]bool{n: true}
		for p := t.parents[n]; p != "" && !seen[p]; p = t.parents[p] {
			seen[p] = true
			out[n] = append(out[n], p)
		}
	}
	return out
}

// LoadDomains parses the domain files (*.yaml) of a directory.
func LoadDomains(dir string) ([]*Domain, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	var out []*Domain
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		d, err := ParseDomain(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, d)
	}
	return out, nil
}
