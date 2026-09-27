package methodology

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	case !nameRE.MatchString(d.Name):
		add("name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
	}
	if len(d.NodeTypes) == 0 {
		add("nodeTypes", "at least one node type required")
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

// Resolve returns the methodology with the types in force, so that Validate and Compile check its references.
func (m *Methodology) Resolve(types TypeSet) *Methodology {
	out := *m
	out.Types = types
	return &out
}

// Patterns of domain types used inside CEL expressions: `"T" in n.types` and `n.type == "T"` on change
// nodes (variables n, x, r, t, f, c), `l.type == "L"` on links (variable l).
var (
	nodeTypeLiterals = []*regexp.Regexp{
		regexp.MustCompile(`"([^"]+)"\s+in\s+[\w.]+\.types\b`),
		regexp.MustCompile(`\b[nxrtfc]\.type\s*==\s*"([^"]+)"`),
	}
	linkTypeLiterals = []*regexp.Regexp{
		regexp.MustCompile(`\bl\.type\s*==\s*"([^"]+)"`),
	}
)

// checkTypeRef checks a reference to a node type (link false) or a link type against the resolved types: qualified
// and known. It returns the problem, or "" (nothing is checked while the types are not resolved).
func (m *Methodology) checkTypeRef(ref string, link bool) string {
	if m.Types == nil {
		return ""
	}
	r, err := domain.ParseTypeRef(ref)
	if err != nil {
		return err.Error()
	}
	if !r.Qualified() {
		return fmt.Sprintf("type reference %q must be qualified: <namespace>@%s", ref, ref)
	}
	if link && !m.Types.HasLinkType(ref) {
		return fmt.Sprintf("unknown link type %s", ref)
	}
	if !link && !m.Types.HasNodeType(ref) {
		return fmt.Sprintf("unknown node type %s", ref)
	}
	return ""
}

// lintTypeRefs checks the type references a methodology uses in CEL literals and builtin params (ADR 0012).
func (m *Methodology) lintTypeRefs(add func(path, format string, args ...any)) {
	scan := func(path, expr string) {
		for _, re := range nodeTypeLiterals {
			for _, g := range re.FindAllStringSubmatch(expr, -1) {
				if msg := m.checkTypeRef(g[1], false); msg != "" {
					add(path, "%s", msg)
				}
			}
		}
		for _, re := range linkTypeLiterals {
			for _, g := range re.FindAllStringSubmatch(expr, -1) {
				if msg := m.checkTypeRef(g[1], true); msg != "" {
					add(path, "%s", msg)
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
					if s, ok := lt.(string); ok {
						if msg := m.checkTypeRef(s, true); msg != "" {
							add(path+".params.linkTypes", "%s", msg)
						}
					}
				}
			}
		}
	}
}

// Namespaces lists the namespaces a methodology uses: its target namespace and the namespaces of the qualified type
// references it makes (CEL literals, expectations, builtin link types).
func (m *Methodology) Namespaces() []string {
	set := map[string]bool{}
	if m.Namespace != "" {
		set[m.Namespace] = true
	}
	ref := func(s string) {
		if r, err := domain.ParseTypeRef(s); err == nil && r.Qualified() {
			set[r.Namespace] = true
		}
	}
	scan := func(expr string) {
		for _, re := range append(slices.Clone(nodeTypeLiterals), linkTypeLiterals...) {
			for _, g := range re.FindAllStringSubmatch(expr, -1) {
				ref(g[1])
			}
		}
	}
	for _, c := range m.Conditions {
		scan(c.Expr)
	}
	for _, a := range m.Actions {
		scan(a.When)
		scan(a.Utility)
		if e := a.Expects; e != nil {
			scan(e.Where)
			ref(e.Produce.NodeType)
			if e.Link != nil {
				ref(e.Link.Type)
			}
		}
		if lts, ok := a.Params["linkTypes"].([]any); ok {
			for _, lt := range lts {
				if s, ok := lt.(string); ok {
					ref(s)
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	slices.Sort(out)
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

// domainsDirFor finds the "domains" directory beside the methodologies directory of a file: methodologies/x.yaml
// and methodologies/examples/x.yaml both resolve to the sibling of methodologies/.
func domainsDirFor(path string) string {
	dir := filepath.Dir(path)
	for i := 0; i < 3; i++ {
		dir = filepath.Join(dir, "..")
		if st, err := os.Stat(filepath.Join(dir, "domains")); err == nil && st.IsDir() {
			return filepath.Join(dir, "domains")
		}
	}
	return filepath.Join(filepath.Dir(path), "..", "domains")
}

// LoadFile parses a methodology file and resolves its types against the domains of the "domains" directory next to
// the methodology's directory (the layout of the repository: methodologies/ and domains/).
func LoadFile(path string) (*Methodology, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := Parse(src)
	if err != nil {
		return nil, err
	}
	ds, err := LoadDomains(domainsDirFor(path))
	if err != nil {
		return nil, err
	}
	return m.Resolve(DomainTypes(ds...)), nil
}
