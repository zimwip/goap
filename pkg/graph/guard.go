package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// The guard (ADR 0054) is the rule of the graph, in Go, whatever the storage: a database with constraints repeats
// it, a storage without any still gets it. Every repository transaction of a Graph goes through it:
//
//   - every node version, link, branch membership and node origin names the change that writes it, and that change
//     exists;
//   - every node version is owned by an organisational unit and every node was created in a project, nodes of the
//     structures in force; a version that names neither gets them from the version it follows, else from the change
//     that writes it (the unit holding it, the project it acts in); the project of a node never changes;
//   - every change names the unit holding it and the project it acts in, nodes of the structures in force;
//   - every baseline is the result of a change (ADR 0056); what precedes the first change of a namespace is the empty
//     state, the empty baseline id, which nothing stores;
//   - a version is immutable once written (ADR 0079): the working state of a node in a change is a draft, the version is
//     written when the change lands, with its outgoing links, in the same transaction; no version is ever edited,
//     frozen or dropped, and only a version written in the transaction gets links.
//
// What names a change, a unit or a project is checked when the transaction ends, before it commits (as a deferred
// foreign key): the bootstrap writes the root unit and the root project, each referencing the other and itself, in
// one transaction.

type guardRepo struct {
	Repo
	g *Graph
}

func (r *guardRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	return r.Repo.InTx(ctx, func(tx Tx) error {
		gt := &guardTx{Tx: tx, g: r.g}
		if err := fn(gt); err != nil {
			return err
		}
		return gt.verify(ctx)
	})
}

type guardTx struct {
	Tx
	g *Graph
	// what the transaction wrote that names a change, a unit or a project, checked at its end
	changeRefs map[domain.ChangeID]string
	owners     map[domain.NodeID]string
	projects   map[domain.NodeID]string
	changes    map[domain.ChangeID]domain.Change
	// headers caches the changes read to stamp versions
	headers map[domain.ChangeID]domain.Change
	// written are the versions the transaction wrote
	written map[domain.NodeRef]bool
	// logged are the changes whose log the transaction appended to (the draft cache, ADR 0079, never publishes what a
	// transaction that may roll back has seen of its own writes)
	logged map[domain.ChangeID]bool
}

func (t *guardTx) AppendLog(ctx context.Context, e domain.LogEntry) (domain.LogEntry, error) {
	if t.logged == nil {
		t.logged = map[domain.ChangeID]bool{}
	}
	t.logged[e.Change] = true
	return t.Tx.AppendLog(ctx, e)
}

func (t *guardTx) wroteLog(id domain.ChangeID) bool { return t.logged[id] }

func (t *guardTx) needChange(id domain.ChangeID, what string) error {
	if id == "" {
		return fmt.Errorf("%s is written outside of a change (ADR 0049, 0054): %w", what, ErrInvalid)
	}
	if t.changeRefs == nil {
		t.changeRefs = map[domain.ChangeID]string{}
	}
	if _, ok := t.changeRefs[id]; !ok {
		t.changeRefs[id] = what
	}
	return nil
}

// change reads the header of a change (cached for the transaction).
func (t *guardTx) change(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	if c, ok := t.headers[id]; ok {
		return c, nil
	}
	c, err := t.Tx.Change(ctx, id)
	if err != nil {
		return c, err
	}
	if t.headers == nil {
		t.headers = map[domain.ChangeID]domain.Change{}
	}
	t.headers[id] = c
	return c, nil
}

// resolve returns the id of the node of a structure a change names by key; the node being written is its own
// reference (the bootstrap).
func (t *guardTx) resolve(ctx context.Context, kind, key string, self domain.Node) (domain.NodeID, error) {
	st := t.g.Structure(kind)
	if key == self.Key && st.Namespace == domain.NamespaceOf(self.Namespace) {
		return self.ID, nil
	}
	id, err := t.Tx.NodeIDByKey(ctx, st.Namespace, key)
	if errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("node %s: %s %q of its change does not exist: %w", self.Key, kind, key, ErrInvalid)
	}
	return id, err
}

func (t *guardTx) PutNode(ctx context.Context, n domain.Node) error {
	what := "node " + n.Key + " v" + fmt.Sprint(n.Version)
	if err := t.needChange(n.ChangeID, what); err != nil {
		return err
	}
	var prev *domain.Node
	if n.Version > 1 {
		vs, err := t.Tx.Versions(ctx, n.ID)
		if err != nil {
			return err
		}
		if len(vs) > 0 {
			p := vs[len(vs)-1]
			if len(n.Parents) > 0 && int(n.Parents[0]) >= 1 && int(n.Parents[0]) <= len(vs) {
				p = vs[n.Parents[0]-1]
			}
			prev = &p
			if n.Project != "" && n.Project != vs[0].Project {
				return fmt.Errorf("%s: the project of a node never changes (%s, not %s): %w", what, vs[0].Project, n.Project, ErrInvalid)
			}
			n.Project = vs[0].Project
		}
	}
	if n.Owner == "" && prev != nil {
		n.Owner = prev.Owner
	}
	if n.Owner == "" || n.Project == "" {
		c, err := t.change(ctx, n.ChangeID)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%s: change %s does not exist: %w", what, n.ChangeID, ErrInvalid)
		}
		if err != nil {
			return err
		}
		if n.Owner == "" {
			if n.Owner, err = t.resolve(ctx, domain.StructureOrganisation, c.OwnerOrg, n); err != nil {
				return err
			}
		}
		if n.Project == "" {
			if n.Project, err = t.resolve(ctx, domain.StructureProject, c.ProjectID, n); err != nil {
				return err
			}
		}
	}
	if t.owners == nil {
		t.owners, t.projects = map[domain.NodeID]string{}, map[domain.NodeID]string{}
	}
	t.owners[n.Owner] = what
	t.projects[n.Project] = what
	if err := t.Tx.PutNode(ctx, n); err != nil {
		return err
	}
	if t.written == nil {
		t.written = map[domain.NodeRef]bool{}
	}
	t.written[n.Ref()] = true
	return nil
}

func (t *guardTx) PutTag(ctx context.Context, tag domain.Tag) error {
	if err := t.needChange(tag.ChangeID, "tag "+tag.Name); err != nil {
		return err
	}
	return t.Tx.PutTag(ctx, tag)
}

func (t *guardTx) PutLink(ctx context.Context, l domain.Link) error {
	if err := t.needChange(l.ChangeID, "link "+l.Type); err != nil {
		return err
	}
	if !t.written[l.From] {
		return fmt.Errorf("link %s: version %s is not written by this transaction, a version never changes (ADR 0079): %w", l.Type, l.From, ErrConflict)
	}
	return t.Tx.PutLink(ctx, l)
}

func (t *guardTx) SetNodeOrigin(ctx context.Context, ref domain.NodeRef, change domain.ChangeID, cn domain.ChangeImpactID, comment string) error {
	if err := t.needChange(change, "the origin of node "+ref.String()); err != nil {
		return err
	}
	return t.Tx.SetNodeOrigin(ctx, ref, change, cn, comment)
}

func (t *guardTx) JoinBranch(ctx context.Context, ref domain.NodeRef, branch string, change domain.ChangeID) error {
	if err := t.needChange(change, "node "+ref.String()+" joining "+branch); err != nil {
		return err
	}
	return t.Tx.JoinBranch(ctx, ref, branch, change)
}

func (t *guardTx) PutChange(ctx context.Context, c domain.Change) error {
	if c.OwnerOrg == "" || c.ProjectID == "" {
		return fmt.Errorf("change %s names no owner unit or no project (ADR 0054): %w", c.ID, ErrInvalid)
	}
	if err := t.Tx.PutChange(ctx, c); err != nil {
		return err
	}
	if t.changes == nil {
		t.changes = map[domain.ChangeID]domain.Change{}
	}
	h := c
	h.Items, h.Nodes = nil, nil
	t.changes[c.ID] = h
	delete(t.headers, c.ID)
	return nil
}

func (t *guardTx) DeleteChange(ctx context.Context, id domain.ChangeID, namespace, branch string) error {
	// the store checks that no baseline holds what the change wrote from the entries: every baseline of the namespace
	// must have its own (a baseline kept as a header only is materialised first)
	bs, err := t.Tx.Baselines(ctx, namespace)
	if err != nil {
		return err
	}
	for _, b := range bs {
		if err := t.materialize(ctx, b.ID); err != nil {
			return err
		}
	}
	if err := t.Tx.DeleteChange(ctx, id, namespace, branch); err != nil {
		return err
	}
	delete(t.changes, id)
	delete(t.headers, id)
	return nil
}

// verify checks, at the end of the transaction, that what it wrote names changes, units and projects that exist.
func (t *guardTx) verify(ctx context.Context) error {
	for id, what := range t.changeRefs {
		if _, ok := t.changes[id]; ok {
			continue
		}
		if _, err := t.change(ctx, id); errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%s names change %s, which does not exist: %w", what, id, ErrInvalid)
		} else if err != nil {
			return err
		}
	}
	for id, what := range t.owners {
		if err := t.inStructure(ctx, domain.StructureOrganisation, id, what+": owner"); err != nil {
			return err
		}
	}
	for id, what := range t.projects {
		if err := t.inStructure(ctx, domain.StructureProject, id, what+": project"); err != nil {
			return err
		}
	}
	for _, c := range t.changes {
		for _, ref := range []struct{ kind, key, what string }{
			{domain.StructureOrganisation, c.OwnerOrg, "owner"},
			{domain.StructureProject, c.ProjectID, "project"},
		} {
			st := t.g.Structure(ref.kind)
			id, err := t.Tx.NodeIDByKey(ctx, st.Namespace, ref.key)
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("change %s: %s %q is not a node of namespace %s: %w", c.ID, ref.what, ref.key, st.Namespace, ErrInvalid)
			}
			if err != nil {
				return err
			}
			if err := t.inStructure(ctx, ref.kind, id, "change "+string(c.ID)+": "+ref.what); err != nil {
				return err
			}
		}
	}
	return nil
}

// inStructure checks that a node exists (on any branch) and belongs to the structure of a kind.
func (t *guardTx) inStructure(ctx context.Context, kind string, id domain.NodeID, what string) error {
	st := t.g.Structure(kind)
	vs, err := t.Tx.Versions(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && len(vs) == 0) {
		return fmt.Errorf("%s %s does not exist: %w", what, id, ErrInvalid)
	}
	if err != nil {
		return err
	}
	if n := vs[len(vs)-1]; !t.g.inStructure(n, st) {
		return fmt.Errorf("%s %s is a %s of namespace %s, not a %s: %w", what, n.Key, n.Type, domain.NamespaceOf(n.Namespace), st.Type, ErrInvalid)
	}
	return nil
}
