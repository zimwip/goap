package gitimport

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// gitLine is a git branch imported besides Rev's (ADR 0151): the branch of the graph it goes to and its last commit.
type gitLine struct {
	git, graph, tip string
	// estimate is what Stats.Total counted for it before the run reached it: its commits not on Rev's line
	estimate int
}

// lines are the git branches Branches names, but the one Rev follows, by name.
func (im *Importer) lines(ctx context.Context) ([]gitLine, error) {
	if len(im.Branches) == 0 {
		return nil, nil
	}
	for _, p := range im.Branches {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("gitimport: branch pattern %q: %w", p, err)
		}
	}
	refs, err := Branches(ctx, im.Dir)
	if err != nil {
		return nil, err
	}
	main := BranchOf(ctx, im.Dir, im.Rev)
	var out []gitLine
	for _, r := range refs {
		if r.Name == main || !slices.ContainsFunc(im.Branches, func(p string) bool {
			ok, _ := path.Match(p, r.Name)
			return p == "*" || ok
		}) {
			continue
		}
		out = append(out, gitLine{git: r.Name, graph: GraphBranch(im.Name, r.Name), tip: r.Hash})
	}
	return out, nil
}

// ParseBranches reads a list of git branches given as text (Importer.Branches): names or patterns separated by commas
// or spaces, "all" for every branch.
func ParseBranches(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if f == "all" {
			f = "*"
		}
		out = append(out, f)
	}
	return out
}

// GraphBranch names the branch of the graph a git branch of a repository is imported on: the branches of a namespace
// are shared by the repositories it holds, main being the line of each one's Rev.
func GraphBranch(repo, branch string) string { return repo + "/" + branch }

// line imports a git branch on its branch of the graph, opened at its fork the first time.
func (im *Importer) line(ctx context.Context, l gitLine) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if im.Max > 0 && im.stats.Commits >= im.Max {
		return nil
	}
	im.phase("importing the git branch " + l.git)
	if _, err := im.Graph.Branch(ctx, Namespace, l.graph); errors.Is(err, graphapi.ErrNotFound) {
		fork, why, err := im.fork(ctx, l)
		if err != nil {
			return fmt.Errorf("branch %s: %w", l.git, err)
		}
		if why != "" {
			im.skip(l, why)
			return nil
		}
		if _, err := im.Graph.CreateBranch(ctx, graphapi.NewBranch{Name: l.graph, Namespace: Namespace, From: fork,
			Origin: "gitimport:" + im.Name, Description: fmt.Sprintf("git branch %s of %s", l.git, im.Name)}); err != nil {
			return fmt.Errorf("branch %s: %w", l.git, err)
		}
	} else if err != nil {
		return fmt.Errorf("branch %s: %w", l.git, err)
	}
	im.branch, im.gitRef = l.graph, l.git
	if err := im.load(ctx); err != nil {
		return fmt.Errorf("branch %s: %w", l.git, err)
	}
	from := im.ix.commit
	if from != "" {
		ok, err := IsAncestor(ctx, im.Dir, from, l.tip)
		if err != nil {
			return fmt.Errorf("branch %s: %w", l.git, err)
		}
		if !ok {
			im.skip(l, "rewritten since its last import ("+short(from)+" is no longer in its history)")
			return nil
		}
	}
	// the estimate becomes the count (a bounded run's Total is its bound)
	if im.Max == 0 {
		n, err := Count(ctx, im.Dir, from, l.tip)
		if err != nil {
			return err
		}
		im.stats.Total += n - l.estimate
	}
	if err := im.history(ctx, from, l.tip); err != nil {
		return fmt.Errorf("branch %s: %w", l.git, err)
	}
	im.stats.Branches++
	return nil
}

func (im *Importer) skip(l gitLine, why string) {
	im.stats.Skipped = append(im.stats.Skipped, l.git+": "+why)
	if im.Max == 0 {
		im.stats.Total -= l.estimate
	}
	im.phase("git branch " + l.git + " left out: " + why)
}

// versionPage is how many versions of the repository node fork reads at a time.
const versionPage = 500

// fork is the baseline a git branch starts from: the state the import of the last commit of its first-parent line
// already imported left (the repository node, written by every commit, names the commit of each of its versions); the
// empty state for a branch sharing no commit with what was imported, when the import starts at the root commit; why
// says why the branch is left out otherwise.
func (im *Importer) fork(ctx context.Context, l gitLine) (domain.BaselineID, string, error) {
	repo := im.ix.byKey[im.Name]
	if repo != nil {
		line, err := FirstParents(ctx, im.Dir, l.tip)
		if err != nil {
			return "", "", err
		}
		for before := domain.Version(0); ; {
			vs, next, err := im.Graph.VersionPage(ctx, repo.id, before, versionPage)
			if err != nil {
				return "", "", err
			}
			for i := len(vs) - 1; i >= 0; i-- {
				commit, _ := vs[i].Properties["commit"].(string)
				if !line[commit] {
					continue
				}
				if b, err := im.landed(ctx, vs[i].ChangeID); err != nil || b != "" {
					return b, "", err
				}
			}
			if next == 0 {
				break
			}
			before = next
		}
	}
	if im.Base != "" {
		return "", "it forks before the first commit imported", nil
	}
	return "", "", nil
}

// landed is the baseline the change of a commit left on the branch it was imported on: the change of a version, or the
// change of the commit whose sub-change wrote it; "" when it never landed (abandoned).
func (im *Importer) landed(ctx context.Context, id domain.ChangeID) (domain.BaselineID, error) {
	for id != "" {
		c, err := im.Graph.Change(ctx, id)
		if err != nil {
			return "", err
		}
		if c.ParentID == "" {
			if c.Status != domain.ChangeApplied {
				return "", nil
			}
			return c.ResultBaselineID, nil
		}
		id = c.ParentID
	}
	return "", nil
}
