package access

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/lib/typecat"
)

// ProjectScopeValidator keeps the writes of Assignments and projects within the rights of the person committing them
// (ADR 0135), judged on the state before the change (the versions it starts from): a platform Assignment is a platform
// administrator's; an Assignment of a project, its administrators' (the flags, its owners'); a new project, the
// administrators' of its parent; an edit of a project, its administrators'; stopping or restarting it, its owners';
// its move, the owners' of its old and its new parent. A platform service acting by itself and a platform administrator
// are not asked. The handler's gate is the early refusal, this the guarantee: every path writing these nodes commits.
// It reads through the query of the commit only: the stores are not reentrant.
type ProjectScopeValidator struct{}

// Types are the nodes whose writes the validator judges.
func (ProjectScopeValidator) Types() []string {
	return []string{NodeTypeAssignment, NodeTypeProjectUnit}
}

// Validate implements graphapi.NodeValidator.
func (v ProjectScopeValidator) Validate(ctx context.Context, q graphapi.ValidatorQuery, impacted []graphapi.ValidatedNode) error {
	who := authz.From(ctx)
	for _, n := range impacted {
		// whoever writes them, a platform Assignment carries no project flag
		if n.Post.Type == NodeTypeAssignment && n.Post.State != StateRetired && (flag(n.Post, PropOwner) || flag(n.Post, PropAdmin)) {
			if p, err := linkedKey(ctx, q, n.Post, LinkAssignsProject); err != nil {
				return err
			} else if p == "" {
				return fmt.Errorf("assignment %s: a platform assignment carries no owner or admin flag: %w", n.Post.Key, graphapi.ErrInvalid)
			}
		}
	}
	if who.System() {
		return nil
	}
	_, before, err := snapshots(ctx, q, impacted)
	if err != nil {
		return fmt.Errorf("the rights on the projects cannot be checked: %v: %w", err, authz.ErrForbidden)
	}
	who = before.Enrich(who)
	if who.HasRole(RoleAdmin) {
		return nil
	}
	subject := before.SubjectChain(who)
	chainOf := func(project string) ([]string, error) { return projectChain(ctx, q, before, project) }
	need := func(project string, owner bool, what string) error {
		chain, err := chainOf(project)
		if err != nil {
			return err
		}
		admin, own := before.ProjectFlags(subject, chain)
		if (owner && !own) || !admin {
			right := "administer"
			if owner {
				right = "own"
			}
			return fmt.Errorf("%s needs to %s the project %s: %w", what, right, project, authz.ErrForbidden)
		}
		return nil
	}
	for _, n := range impacted {
		switch n.Post.Type {
		case NodeTypeAssignment:
			if err := v.assignment(ctx, q, before, n, need); err != nil {
				return err
			}
		case NodeTypeProjectUnit:
			if err := v.project(ctx, q, before, n, need); err != nil {
				return err
			}
		}
	}
	return nil
}

func (ProjectScopeValidator) assignment(ctx context.Context, q graphapi.ValidatorQuery, before *Snapshot, n graphapi.ValidatedNode, need func(string, bool, string) error) error {
	what := "assignment " + n.Post.Key
	post, err := linkedKey(ctx, q, n.Post, LinkAssignsProject)
	if err != nil {
		return err
	}
	retired := n.Post.State == StateRetired
	postOwner, postAdmin := !retired && flag(n.Post, PropOwner), !retired && flag(n.Post, PropAdmin)
	pre, preOwner, preAdmin, existed := before.AssignmentProject(n.Post.Key)
	if post == "" || pre == PlatformScope {
		return fmt.Errorf("%s: a platform assignment is a platform administrator's: %w", what, authz.ErrForbidden)
	}
	flags := postOwner != preOwner || postAdmin != preAdmin
	projects := []string{post}
	if existed && pre != post {
		projects = append(projects, pre)
	}
	for _, p := range projects {
		if err := need(p, flags, what); err != nil {
			return err
		}
	}
	return nil
}

func (ProjectScopeValidator) project(ctx context.Context, q graphapi.ValidatorQuery, before *Snapshot, n graphapi.ValidatedNode, need func(string, bool, string) error) error {
	key := n.Post.Key
	what := "project " + key
	parent, err := linkedKey(ctx, q, n.Post, LinkProjectPartOf)
	if err != nil {
		return err
	}
	if parent == key {
		parent = ""
	}
	if n.Pre == nil || !before.HasProject(key) {
		if parent == "" {
			return fmt.Errorf("%s: a root project is a platform administrator's: %w", what, authz.ErrForbidden)
		}
		return need(parent, false, "creating "+what)
	}
	was := before.ProjectOf(key)
	if was == "" {
		return fmt.Errorf("%s: the root project is a platform administrator's: %w", what, authz.ErrForbidden)
	}
	if parent != was {
		if parent == "" {
			return fmt.Errorf("%s: a root project is a platform administrator's: %w", what, authz.ErrForbidden)
		}
		for _, p := range []string{was, parent} {
			if err := need(p, true, "moving "+what); err != nil {
				return err
			}
		}
	}
	if flag(*n.Pre, PropStopped) != flag(n.Post, PropStopped) {
		return need(key, true, "stopping or restarting "+what)
	}
	return need(key, false, "editing "+what)
}

// ProjectOwnerValidator refuses a change that leaves a project that had an owner with none (ADR 0135): an owner
// Assignment on it or above it, as the change leaves the graph. A project never owned (a platform administrator's
// alone) is not asked for one.
type ProjectOwnerValidator struct{}

// Types are the nodes that can take the last owner away.
func (ProjectOwnerValidator) Types() []string { return []string{NodeTypeAssignment} }

// Validate implements graphapi.NodeValidator.
func (v ProjectOwnerValidator) Validate(ctx context.Context, q graphapi.ValidatorQuery, impacted []graphapi.ValidatedNode) error {
	after, before, err := snapshots(ctx, q, impacted)
	if err != nil {
		return err
	}
	var projects []string
	for _, n := range impacted {
		if p, owner, _, ok := before.AssignmentProject(n.Post.Key); ok && owner && p != PlatformScope && !slices.Contains(projects, p) {
			projects = append(projects, p)
		}
	}
	for _, p := range projects {
		if before.ProjectOwned(p) && after.HasProject(p) && !after.ProjectOwned(p) {
			return fmt.Errorf("this change would leave the project %s with no owner: %w", p, graphapi.ErrInvalid)
		}
	}
	return nil
}

// snapshots are the snapshots of the organisation as the change leaves it and as it found it: the same nodes, each
// node the change wrote taken back to the version it started from (left out when it created it).
func snapshots(ctx context.Context, q graphapi.ValidatorQuery, impacted []graphapi.ValidatedNode) (after, before *Snapshot, err error) {
	written := map[domain.NodeID]*domain.Node{}
	for _, n := range impacted {
		written[n.Post.ID] = n.Pre
	}
	var nodes, was []domain.Node
	var links, wasLinks []domain.Link
	st := typecat.Builtin().Structures()
	for _, typ := range []string{NodeTypeUser, NodeTypeAssignment, st.Organisation().Type, st.Project().Type} {
		ns, err := q.NodesOfType(ctx, st.Organisation().Namespace, typ)
		if err != nil {
			return nil, nil, err
		}
		for _, n := range ns {
			ls, err := q.OutLinksOf(ctx, n.Ref())
			if err != nil {
				return nil, nil, err
			}
			nodes, links = append(nodes, n), append(links, ls...)
			pre, changed := written[n.ID]
			switch {
			case !changed:
				was, wasLinks = append(was, n), append(wasLinks, ls...)
			case pre != nil:
				pls, err := q.OutLinksOf(ctx, pre.Ref())
				if err != nil {
					return nil, nil, err
				}
				was, wasLinks = append(was, *pre), append(wasLinks, pls...)
			}
		}
	}
	return BuildSnapshot(st, "", nodes, links), BuildSnapshot(st, "", was, wasLinks), nil
}

// projectChain is the chain of a project (itself first): the head's for a project it holds, else the parent the change
// gives a new project, then the head's chain of that parent.
func projectChain(ctx context.Context, q graphapi.ValidatorQuery, before *Snapshot, project string) ([]string, error) {
	var chain []string
	for range 64 {
		if before.HasProject(project) {
			return append(chain, before.ProjectChain(project)...), nil
		}
		chain = append(chain, project)
		n, ok, err := projectNode(ctx, q, project)
		if err != nil || !ok {
			return chain, err
		}
		parent, err := linkedKey(ctx, q, n, LinkProjectPartOf)
		if err != nil || parent == "" || parent == project || slices.Contains(chain, parent) {
			return chain, err
		}
		project = parent
	}
	return chain, nil
}

func projectNode(ctx context.Context, q graphapi.ValidatorQuery, key string) (domain.Node, bool, error) {
	ns, err := q.NodesOfType(ctx, NamespaceOrganisation, NodeTypeProjectUnit)
	if err != nil {
		return domain.Node{}, false, err
	}
	for _, n := range ns {
		if n.Key == key {
			return n, true, nil
		}
	}
	return domain.Node{}, false, nil
}

// linkedKey is the key of the target of the link of a type the node holds, as the change leaves it ("" when none).
func linkedKey(ctx context.Context, q graphapi.ValidatorQuery, n domain.Node, typ string) (string, error) {
	ls, err := q.OutLinksOf(ctx, n.Ref())
	if err != nil {
		return "", err
	}
	for _, l := range ls {
		if l.Type != typ {
			continue
		}
		to, err := q.Node(ctx, l.To)
		if err != nil {
			return "", err
		}
		return to.Key, nil
	}
	return "", nil
}

func flag(n domain.Node, prop string) bool {
	v, _ := n.Properties[prop].(bool)
	return v
}
