package assistantsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/platform/convsvc"
)

// ToolSearchChanges lists the changes of the active project matching a text: what the assistant shows before it
// proposes a new change.
const ToolSearchChanges = "search_changes"

// ActionSearchChanges is the action a search leaves on the assistant message: the record, read back from the history,
// that the open changes were searched in the conversation (a label in the interface, never run).
const ActionSearchChanges = "search_changes"

const (
	maxSearchResults = 10
	minSearchWord    = 3
)

// defaultSearchStatuses are the open changes: the ones a new piece of work may join.
var defaultSearchStatuses = []domain.ChangeStatus{domain.ChangeDraft, domain.ChangeActive, domain.ChangeCommitted}

var allStatuses = []domain.ChangeStatus{domain.ChangeDraft, domain.ChangeActive, domain.ChangeCommitted, domain.ChangeApplied, domain.ChangeAbandoned}

// subProjects is what a Projects may also tell: a project and the projects below it (Directory does, through
// access.Directory). Without it the search covers the active project only.
type subProjects interface {
	SubProjects(ctx context.Context, project string) ([]string, error)
}

// changeHit is a change as search_changes returns it.
type changeHit struct {
	ChangeID    string `json:"changeId"`
	Title       string `json:"title"`
	Intent      string `json:"intent,omitempty"`
	Status      string `json:"status"`
	State       string `json:"state,omitempty"`
	Methodology string `json:"methodology,omitempty"`
	Project     string `json:"project"`
	Score       int    `json:"score"`
}

// searchWords are the distinct lower-case words of a query of at least minSearchWord letters or digits.
func searchWords(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) >= minSearchWord && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// searchScore is the lexical score of a change for the words: per word, 2 when the title holds it, 1 when the intent
// does (case-insensitive).
func searchScore(words []string, c domain.Change) int {
	title, intent := strings.ToLower(c.Title), strings.ToLower(c.Intent)
	n := 0
	for _, w := range words {
		if strings.Contains(title, w) {
			n += 2
		}
		if strings.Contains(intent, w) {
			n++
		}
	}
	return n
}

// statusesOf reads the optional "status" argument: one status or a list; the open ones by default.
func statusesOf(a args) ([]domain.ChangeStatus, error) {
	var raw []any
	switch v := a["status"].(type) {
	case nil:
		return defaultSearchStatuses, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return defaultSearchStatuses, nil
		}
		raw = []any{v}
	case []any:
		raw = v
	default:
		return nil, errors.New(`"status" is a status or a list of statuses`)
	}
	var out []domain.ChangeStatus
	for _, r := range raw {
		s, _ := r.(string)
		st := domain.ChangeStatus(strings.ToLower(strings.TrimSpace(s)))
		if !slices.Contains(allStatuses, st) {
			return nil, fmt.Errorf("unknown status %q: the statuses are draft, active, committed, applied, abandoned", s)
		}
		if !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	if len(out) == 0 {
		return defaultSearchStatuses, nil
	}
	return out, nil
}

// projectsToSearch are the projects of the search: the active one (the root when none is named) and, when the
// directory tells them, the ones below it the caller may work on.
func (t *turn) projectsToSearch(ctx context.Context) ([]string, error) {
	project := t.project
	if project == "" {
		var err error
		if project, err = t.s.Projects.RootProject(ctx); err != nil {
			return nil, err
		}
	} else if ok, err := t.s.Projects.MayAccessProject(ctx, t.p, project); err != nil || !ok {
		return nil, fmt.Errorf("you may not work on project %q", project)
	}
	out := []string{project}
	sp, ok := t.s.Projects.(subProjects)
	if !ok {
		return out, nil
	}
	subs, err := sp.SubProjects(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("the organisation cannot be read: %w", err)
	}
	for _, s := range subs {
		if s == project || slices.Contains(out, s) {
			continue
		}
		if ok, err := t.s.Projects.MayAccessProject(ctx, t.p, s); err == nil && ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// searchChanges lists the changes of the active project chain matching q, as the caller, best first.
func (t *turn) searchChanges(ctx context.Context, a args) (any, error) {
	for k := range a {
		if k != "q" && k != "status" {
			return nil, fmt.Errorf(`unknown argument %q: the arguments are q, status`, k)
		}
	}
	q := a.str("q")
	words := searchWords(q)
	if len(words) == 0 {
		return nil, fmt.Errorf(`"q" needs at least one word of %d characters or more`, minSearchWord)
	}
	statuses, err := statusesOf(a)
	if err != nil {
		return nil, err
	}
	projects, err := t.projectsToSearch(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := t.s.Graph.ListChanges(authz.With(ctx, t.p), graph.ChangesFilter{Status: statuses})
	if err != nil {
		return nil, fmt.Errorf("the changes cannot be listed: %w", err)
	}
	var hits []changeHit
	for _, c := range cs {
		if !slices.Contains(projects, c.ProjectID.Key) || (access.IsPersonal(c) && !access.IsPersonalTo(c, t.p.Subject)) {
			continue
		}
		score := searchScore(words, c)
		if score == 0 {
			continue
		}
		hits = append(hits, changeHit{ChangeID: string(c.ID), Title: clip(c.Title, 200), Intent: clip(c.Intent, 200), Status: string(c.Status),
			State: c.State, Methodology: c.Methodology, Project: c.ProjectID.Key, Score: score})
	}
	slices.SortStableFunc(hits, func(x, y changeHit) int {
		if x.Score != y.Score {
			return y.Score - x.Score
		}
		return strings.Compare(x.ChangeID, y.ChangeID)
	})
	total := len(hits)
	hits = hits[:min(total, maxSearchResults)]
	if hits == nil {
		hits = []changeHit{}
	}
	st := make([]string, len(statuses))
	for i, s := range statuses {
		st[i] = string(s)
	}
	res := map[string]any{"q": q, "projects": projects, "statuses": st, "total": total, "changes": hits}
	if total == 0 {
		res["note"] = "no change matches: tell the person, and create a change only if they want one"
	} else {
		res["note"] = "show the person what was found; propose to open one that fits, and create a change only if none fits or they say so"
	}
	t.searched = true
	t.actions = append(t.actions, convsvc.Action{"type": ActionSearchChanges, "label": clip(fmt.Sprintf("Searched the changes for %q (%d found)", q, total), 200),
		"args": map[string]any{"q": q}, "result": map[string]any{"count": total}})
	return res, nil
}
