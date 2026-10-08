package methodology

import (
	"fmt"
	"github.com/zimwip/goap/pkg/domain/def"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/zimwip/goap/pkg/builtins"
	"github.com/zimwip/goap/pkg/domain"
)

// Resolve returns the methodology with the types in force, so that Validate and Compile check its references.
func (m *Methodology) Resolve(types def.TypeSet) *Methodology {
	out := *m
	out.Types = types
	return &out
}

// BuiltinSet tells which builtin names exist (ADR 0062): the static list of pkg/builtins, or the registry of an engine.
type BuiltinSet interface{ HasBuiltin(name string) bool }

// WithBuiltins returns the methodology with the builtins in force, so that Validate and Compile refuse an action
// naming an unknown one.
func (m *Methodology) WithBuiltins(b BuiltinSet) *Methodology {
	out := *m
	out.Builtins = b
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

// checkObjectTypeRef checks a reference to a change object type (ADR 0098): qualified and known, when the types are
// resolved and know change object types. It returns the problem, or "".
func (m *Methodology) checkObjectTypeRef(ref string) string {
	r, err := domain.ParseTypeRef(ref)
	if err != nil {
		return err.Error()
	}
	if !r.Qualified() {
		return fmt.Sprintf("type reference %q must be qualified: <namespace>@%s", ref, ref)
	}
	if ot, ok := m.Types.(def.ObjectTypeSet); ok && !ot.HasObjectType(ref) {
		return fmt.Sprintf("unknown change object type %s", ref)
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
	for i, me := range m.Methods {
		scan(fmt.Sprintf("methods[%d].when", i), me.When)
	}
	for i, a := range m.Actions {
		path := fmt.Sprintf("actions[%d]", i)
		scan(path+".when", a.When)
		scan(path+".utility", a.Utility)
		if e := a.Expects; e != nil {
			scan(path+".expects.where", e.Where)
		}
		// the type references in the params of a builtin: only graph.propagate has any today; a params schema per
		// builtin is a future step (ADR 0062).
		if a.Kind == KindBuiltin && a.Builtin == builtins.GraphPropagate {
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
	ds, err := def.LoadDomains(domainsDirFor(path))
	if err != nil {
		return nil, err
	}
	return m.Resolve(def.DomainTypes(ds...)), nil
}
