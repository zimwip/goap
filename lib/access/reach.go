package access

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/cache"
)

// ReachSet is what a principal reads of the content of the graphs (ADR 0161 §5), compiled once from the snapshot:
// the versions whose scope reaches it (domain.Visibility...). Its keys are the structures' keys.
type ReachSet struct {
	// Projects are the projects it works on (MayAccessProject: an Assignment on the project or one above it): their
	// versions of scope project, and private or personal ones when Units or Subject match.
	Projects []string
	// Trees are the projects whose versions of scope tree it reads: the children of every project above a project it
	// works on, itself included (the parent tree of a project, siblings included).
	Trees []string
	// Units are its own unit (its personal unit), the unit it belongs to and the units above it: the private versions
	// they answer for.
	Units []string
	// Subject is the principal: the personal versions of the nodes it created.
	Subject string
}

// ReachSet compiles the reach of a principal (ADR 0161 §5); empty for an anonymous one.
func (s *Snapshot) ReachSet(p authz.Principal) ReachSet {
	if p.Anonymous() {
		return ReachSet{}
	}
	r := ReachSet{Projects: s.ReachableProjects(p), Units: s.SubjectChain(p), Subject: p.Subject}
	children := map[string][]string{}
	for k := range s.projectMethods {
		if chain := s.ProjectChain(k); len(chain) > 1 {
			children[chain[1]] = append(children[chain[1]], k)
		}
	}
	trees := map[string]bool{}
	for _, w := range r.Projects {
		for _, a := range s.ProjectChain(w) {
			for _, c := range children[a] {
				trees[c] = true
			}
		}
	}
	for k := range trees {
		r.Trees = append(r.Trees, k)
	}
	sort.Strings(r.Trees)
	return r
}

// reachKey keys the reach sets the directory keeps: a principal at a head of the organisation.
type reachKey struct {
	subject, org string
	commit       string
}

// ReachBounds are the bounds of the reach sets a directory keeps (ADR 0156): a set per person reading at a head of the
// organisation, gone once nobody read it for a while; a new head is a new key.
var ReachBounds = cache.Bounds{Bytes: 8 << 20, TTL: 10 * time.Minute}

// ReachSet is the reach of a principal at the current head of the organisation (Snapshot.ReachSet), kept per subject
// and head (ADR 0161 §5): a commit event the directory follows moves the head, and the next read compiles it again.
func (d *Directory) ReachSet(ctx context.Context, p authz.Principal) (ReachSet, error) {
	s, err := d.Snapshot(ctx)
	if err != nil {
		return ReachSet{}, err
	}
	d.mu.Lock()
	if d.reach == nil {
		d.reach = cache.New(cache.Fixed(ReachBounds), func(k reachKey, r ReachSet) int64 {
			n := int64(cache.StructBytes + len(k.subject) + len(k.org) + len(k.commit) + len(r.Subject))
			for _, l := range [][]string{r.Projects, r.Trees, r.Units} {
				for _, x := range l {
					n += cache.WordBytes + int64(len(x))
				}
			}
			return n
		})
	}
	c := d.reach
	d.mu.Unlock()
	k := reachKey{subject: p.Subject, org: p.Org, commit: string(s.Commit)}
	if r, ok := c.Get(k); ok {
		return r, nil
	}
	r := s.ReachSet(p)
	c.Put(k, r)
	return r, nil
}

// Holds reports whether the reach holds a version by the keys a reader stores of it (an index document, ADR 0161 §5):
// the project of the node, the scope of the version (empty: project), the unit answering for it and the subject who
// created the node. The graph's view reader decides the same with graph.Reach.
func (r ReachSet) Holds(project, visibility, responsible, creator string) bool {
	in := func(l []string, k string) bool { return k != "" && slices.Contains(l, k) }
	switch domain.VisibilityOf(visibility) {
	case domain.VisibilityPublic:
		return true
	case domain.VisibilityTree:
		return in(r.Trees, project) || in(r.Projects, project)
	case domain.VisibilityPrivate:
		return in(r.Projects, project) && in(r.Units, responsible)
	case domain.VisibilityPersonal:
		return in(r.Projects, project) && r.Subject != "" && creator == r.Subject
	default:
		return in(r.Projects, project)
	}
}
