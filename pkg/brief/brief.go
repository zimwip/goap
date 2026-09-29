// Package brief renders what a model needs to know of a change in few tokens (ADR 0036 §2): one line per fact, no
// JSON, bounded, the most important first; and traces an information through the change (where it comes from, what
// it led to).
package brief

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Step is the step of a process the reader carries out.
type Step struct {
	Process, Path, Method, Responsible, Accountable string
}

// Limits bound the brief.
const (
	maxImpacts   = 40
	maxArtifacts = 8
	maxRisks     = 15
	maxActions   = 15
	maxText      = 90
)

// ArtifactTypesSkipped are the bookkeeping artifacts the brief leaves out.
var ArtifactTypesSkipped = []string{"step_done", "guidance"}

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// typeName drops the namespace of a qualified type when it is the change's.
func typeName(t, ns string) string {
	if n, rest, ok := strings.Cut(t, domain.TypeSep); ok && n == ns {
		return rest
	}
	return t
}

// Of renders the brief of a change as seen on a blackboard, for a reader carrying out step (nil: none).
func Of(bb domain.Blackboard, step *Step) string {
	c := bb.Change
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("CHANGE %s %q status=%s ns=%s goal=%s", shortID(string(c.ID)), short(c.Title, maxText), c.Status, c.Namespace, c.Goal)
	if c.Intent != "" {
		line("INTENT %s", short(c.Intent, 400))
	}
	if step != nil {
		s := fmt.Sprintf("STEP %s", step.Path)
		if step.Method != "" {
			s += " method=" + step.Method
		}
		if step.Responsible != "" {
			s += " R=" + step.Responsible
		}
		if step.Accountable != "" {
			s += " A=" + step.Accountable
		}
		line("%s", s)
	}
	if bb.ActiveOption != "" {
		line("OPTION %s", bb.ActiveOption)
	}

	// change impacts: the node, its type, what the change does, how far it went, the review
	var impacts []domain.ChangeImpact
	for _, n := range c.Nodes {
		if !n.Superseded {
			impacts = append(impacts, n)
		}
	}
	if len(impacts) > 0 {
		line("IMPACTS %d (key type intent state review)", len(impacts))
		for i, n := range impacts {
			if i == maxImpacts {
				line("- … %d more", len(impacts)-maxImpacts)
				break
			}
			state := "planned"
			switch {
			case n.Landed != nil:
				state = "landed"
			case n.Post != nil:
				state = "written"
			}
			s := fmt.Sprintf("- %s %s %s %s %s", n.Key, typeName(n.Type, c.Namespace), n.Intent, state, n.Review)
			if n.Rationale != "" {
				s += " — " + short(n.Rationale, 60)
			}
			line("%s", s)
		}
	}

	// decision points and their open questions
	for _, d := range bb.DecisionPoints {
		if d.Status == "decided" {
			line("DECIDED %s %q → %s", d.ID, short(d.Question, 70), d.Option)
			continue
		}
		s := fmt.Sprintf("DECISION %s %s %q", d.ID, d.Status, short(d.Question, 70))
		if len(d.Options) > 0 {
			s += " options=" + strings.Join(d.Options, "|")
		}
		line("%s", s)
		for _, q := range d.Questions {
			if q.Status == domain.QuestionOpen {
				line("- question %s %q", q.ID, short(q.Text, 70))
			}
		}
	}

	// the risk register, the live and the highest first
	risks := c.Risks()
	if len(risks) > 0 {
		slices.SortStableFunc(risks, func(a, b domain.Risk) int {
			if a.Live() != b.Live() {
				if a.Live() {
					return -1
				}
				return 1
			}
			return cmp.Compare(b.Score(), a.Score())
		})
		line("RISKS %d (key score=PxI status owner title)", len(risks))
		for i, r := range risks {
			if i == maxRisks {
				line("- … %d more", len(risks)-maxRisks)
				break
			}
			s := fmt.Sprintf("- %s %d=%dx%d %s %s %q", r.Key, r.Score(), r.Probability, r.Impact, r.Status, cmp.Or(r.Owner, "-"), short(r.Title, 70))
			if len(r.Actions) > 0 {
				s += " actions=" + strings.Join(r.Actions, ",")
			}
			line("%s", s)
		}
	}
	var open []domain.ActionItem
	done := 0
	for _, a := range c.ActionItems() {
		if a.Status == domain.ActionOpen {
			open = append(open, a)
		} else {
			done++
		}
	}
	if len(open) > 0 || done > 0 {
		line("ACTIONS %d open, %d closed (key owner due for title)", len(open), done)
		for i, a := range open {
			if i == maxActions {
				line("- … %d more", len(open)-maxActions)
				break
			}
			line("- %s %s %s %s %q", a.Key, cmp.Or(a.Owner, "-"), cmp.Or(a.Due, "-"), cmp.Or(a.For, "-"), short(a.Title, 70))
		}
	}

	// the latest artifacts
	var arts []domain.ChangeItem
	for _, it := range c.Items {
		if it.Kind == domain.KindArtifact && c.Active(it.ID) && !slices.Contains(ArtifactTypesSkipped, it.Type) {
			arts = append(arts, it)
		}
	}
	if len(arts) > 0 {
		from := max(0, len(arts)-maxArtifacts)
		line("ARTIFACTS %d (latest: id type summary)", len(arts))
		for _, it := range arts[from:] {
			line("- %s %s %s", shortID(string(it.ID)), cmp.Or(it.Type, "-"), short(summary(it.Data), maxText))
		}
	}
	return b.String()
}

// summary is the most telling text of an artifact's data.
func summary(d map[string]any) string {
	for _, k := range []string{"summary", "text", "title", "markdown", "reason"} {
		if s, ok := d[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return "{" + strings.Join(keys, ",") + "}"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Trace follows an information through a change: an item (by id or id prefix, or a risk / action key) or a node (by
// key). It says what produced it, what it derives from, what derives from it, and what replaced it.
func Trace(c domain.Change, ref string) (string, error) {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	byID := map[domain.ItemID]domain.ChangeItem{}
	for _, it := range c.Items {
		byID[it.ID] = it
	}
	describe := func(it domain.ChangeItem) string {
		s := fmt.Sprintf("%s %s", shortID(string(it.ID)), it.Kind)
		if it.Type != "" {
			s += "/" + it.Type
		}
		if k, _ := it.Data["key"].(string); k != "" {
			s += " " + k
		}
		s += fmt.Sprintf(" [%s] by %s", it.Status, cmp.Or(it.ProducedBy, "?"))
		if it.Execution != "" {
			s += " run " + shortID(it.Execution)
		}
		if t := summary(it.Data); t != "" {
			s += " — " + short(t, 70)
		}
		return s
	}
	// a node of the change
	for _, n := range c.Nodes {
		if n.Key != ref {
			continue
		}
		line("NODE %s %s %s review=%s", n.Key, n.Type, n.Intent, n.Review)
		line("declared by %s%s — %s", cmp.Or(n.ProducedBy, "?"), runOf(n.Execution), short(n.Rationale, 120))
		for _, id := range append(slices.Clone(n.DerivedFrom), n.Items...) {
			if it, ok := byID[id]; ok {
				line("from %s", describe(it))
			}
		}
		if n.Post != nil {
			line("written %s", n.Post)
		}
		for _, r := range n.Reviews {
			line("reviewed %s by %s: %s", r.Status, r.By, short(r.Comment, 80))
		}
		if n.Landed != nil {
			line("landed %s", n.Landed)
		}
		return b.String(), nil
	}
	// an item: by id, id prefix, or record key (every version)
	var hits []domain.ChangeItem
	for _, it := range c.Items {
		k, _ := it.Data["key"].(string)
		if string(it.ID) == ref || (len(ref) >= 6 && strings.HasPrefix(string(it.ID), ref)) || (k != "" && k == ref) {
			hits = append(hits, it)
		}
	}
	if len(hits) == 0 {
		return "", fmt.Errorf("nothing named %q on the change (an item id, a risk or action key, or a node key)", ref)
	}
	for _, it := range hits {
		line("ITEM %s", describe(it))
		for _, id := range it.DerivedFrom {
			if src, ok := byID[id]; ok {
				line("from %s", describe(src))
			}
		}
		for _, other := range c.Items {
			if slices.Contains(other.DerivedFrom, it.ID) {
				line("led to %s", describe(other))
			}
			if slices.Contains(other.Supersedes, it.ID) {
				line("replaced by %s", describe(other))
			}
		}
		for _, n := range c.Nodes {
			if slices.Contains(n.Items, it.ID) || slices.Contains(n.DerivedFrom, it.ID) {
				line("led to node %s (%s %s)", n.Key, n.Intent, n.Review)
			}
		}
	}
	return b.String(), nil
}

func runOf(execution string) string {
	if execution == "" {
		return ""
	}
	return " run " + shortID(execution)
}
