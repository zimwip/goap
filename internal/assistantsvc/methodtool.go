package assistantsvc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/methodology"
)

// ToolMethodologyQuery reads what a methodology declares, filtered and small (ADR 0094).
const ToolMethodologyQuery = "methodology_query"

var methodologyQueryArgs = []string{"methodology", "kind", "name", "q", "parent", "fields", "detail", "limit", "offset"}

// methodologyQuery answers the tool: the methodologies of the active project only (the rule of list_methodologies and
// list_agents), read as the caller through the registry. It changes nothing and runs nothing.
func (t *turn) methodologyQuery(ctx context.Context, a args) (any, error) {
	for k := range a {
		if !slices.Contains(methodologyQueryArgs, k) {
			return nil, fmt.Errorf("unknown argument %q: the arguments are %s", k, strings.Join(methodologyQueryArgs, ", "))
		}
	}
	q := Query{Kind: strings.ToLower(a.str("kind")), Name: a.str("name"), Q: a.str("q"), Parent: a.str("parent"), Detail: strings.ToLower(a.str("detail"))}
	var err error
	if q.Limit, err = a.integer("limit"); err != nil {
		return nil, err
	}
	if q.Offset, err = a.integer("offset"); err != nil {
		return nil, err
	}
	if v, ok := a["fields"]; ok && v != nil {
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf(`"fields" must be a list of field names`)
		}
		for _, f := range list {
			s, ok := f.(string)
			if !ok {
				return nil, fmt.Errorf(`"fields" must be a list of field names`)
			}
			q.Fields = append(q.Fields, strings.TrimSpace(s))
		}
	}
	// the query is checked before anything is read
	if _, err := q.validated(); err != nil {
		return nil, err
	}
	applicable, err := t.applicable(ctx)
	if err != nil {
		return nil, err
	}
	names := applicable
	if name := a.str("methodology"); name != "" {
		if !slices.Contains(applicable, name) {
			return nil, fmt.Errorf("methodology %q does not apply to the active project (see list_methodologies)", name)
		}
		names = []string{name}
	} else if len(names) > maxMethodologys {
		names = names[:maxMethodologys]
	}
	var ms []*methodology.Methodology
	for _, n := range names {
		c, err := t.s.Methodologies.Methodology(ctx, n)
		if err != nil || c == nil || c.Methodology == nil {
			if len(names) == 1 {
				return nil, fmt.Errorf("methodology %q cannot be read", n)
			}
			continue
		}
		ms = append(ms, c.Methodology)
	}
	return RunQuery(q, ms)
}

// integer reads a whole number argument (0 when absent).
func (a args) integer(k string) (int, error) {
	v, ok := a[k]
	if !ok || v == nil {
		return 0, nil
	}
	f, ok := v.(float64)
	if !ok || f != float64(int(f)) {
		return 0, fmt.Errorf("%q must be a whole number", k)
	}
	return int(f), nil
}
