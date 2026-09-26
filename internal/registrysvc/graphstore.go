package registrysvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graphsnap"
	"github.com/zimwip/goap/pkg/metamodel"
)

// Node types of the stored definitions (platform namespace): one node per version of a methodology or of a domain. The
// definition is the node's content; its status (draft, published, archived) a property. The elements a published
// methodology projects (metamodel, keys M:/D:) are derived from it.
const (
	TypeMethodologyVersion = "MethodologyVersion"
	TypeDomainVersion      = "DomainVersion"
)

// statusDeleted marks a deleted draft: a node key is never freed on a versioned graph, so the node stays and a later
// Save of the same version revives it. The stores treat it as absent.
const statusDeleted Status = "deleted"

// MethodologyVersionKey is the key of the node of a methodology version.
func MethodologyVersionKey(name, version string) string { return "MV:" + key(name, version) }

// DomainVersionKey is the key of the node of a domain version.
func DomainVersionKey(name, version string) string { return "DV:" + key(name, version) }

// GraphStore keeps methodology and domain versions as nodes of the graph, changed through changes applied on main: they
// are versioned, journaled and reviewable like any node, and the registry needs no database of its own. It implements
// Store and DomainStore.
type GraphStore struct {
	Graph metamodel.Graph
	Now   func() time.Time

	cache graphsnap.Cache[*defs]
}

var (
	_ Store       = (*GraphStore)(nil)
	_ DomainStore = (*GraphStore)(nil)
)

// NewGraphStore returns a store over a graph (the graph itself, or a client of the graph service).
func NewGraphStore(g metamodel.Graph) *GraphStore {
	s := &GraphStore{Graph: g, Now: time.Now}
	s.cache = graphsnap.Cache[*defs]{Graph: g, Build: buildDefs}
	return s
}

// defs is the versions of a baseline, decoded once per head of main.
type defs struct {
	methodologies map[string]stored[Record]
	domains       map[string]stored[DomainRecord]
	problems      []string
}

type stored[T any] struct {
	node domain.Node
	rec  T
}

func buildDefs(_ domain.BaselineID, nodes []domain.Node, _ []domain.Link) *defs {
	d := &defs{methodologies: map[string]stored[Record]{}, domains: map[string]stored[DomainRecord]{}}
	for _, n := range nodes {
		if n.Namespace != domain.NamespacePlatform {
			continue
		}
		switch n.Type {
		case TypeMethodologyVersion:
			var r Record
			if err := decode(n.Properties, &r.Methodology, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt, &r.UpdatedBy); err != nil {
				d.problems = append(d.problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			d.methodologies[key(r.Methodology.Name, r.Methodology.Version)] = stored[Record]{n, r}
		case TypeDomainVersion:
			var r DomainRecord
			if err := decode(n.Properties, &r.Domain, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.PublishedAt, &r.UpdatedBy); err != nil {
				d.problems = append(d.problems, fmt.Sprintf("%s: %v", n.Key, err))
				continue
			}
			d.domains[key(r.Domain.Name, r.Domain.Version)] = stored[DomainRecord]{n, r}
		}
	}
	return d
}

func decode(props map[string]any, def any, status *Status, created, updated, published *time.Time, by *string) error {
	b, err := json.Marshal(props["definition"])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, def); err != nil {
		return err
	}
	s, _ := props["status"].(string)
	*status = Status(s)
	*by, _ = props["updatedBy"].(string)
	for k, dst := range map[string]*time.Time{"createdAt": created, "updatedAt": updated, "publishedAt": published} {
		if s, _ := props[k].(string); s != "" {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			*dst = t
		}
	}
	return nil
}

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func versionProps(def any, st Status, created, updated, published time.Time, by string) (map[string]any, error) {
	b, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	m := map[string]any{"definition": d, "status": string(st)}
	for k, t := range map[string]time.Time{"createdAt": created, "updatedAt": updated, "publishedAt": published} {
		if v := ts(t); v != nil {
			m[k] = v
		}
	}
	if by != "" {
		m["updatedBy"] = by
	}
	return m, nil
}

// read returns the versions at the head of main, looked at now.
func (s *GraphStore) read(ctx context.Context) (*defs, error) {
	d, _, err := s.cache.Fresh(ctx)
	if d == nil {
		return nil, err
	}
	return d, nil
}

// commit applies the items build makes from the current versions as one change on main, once more on the new head when
// main moved meanwhile.
func (s *GraphStore) commit(ctx context.Context, title string, build func(*defs) ([]domain.ChangeItem, error)) error {
	for attempt := 0; ; attempt++ {
		d, err := s.read(ctx)
		if err != nil {
			return err
		}
		items, err := build(d)
		if err != nil || len(items) == 0 {
			return err
		}
		head, err := s.Graph.BranchHead(ctx, domain.MainBranch)
		if errors.Is(err, graph.ErrNotFound) {
			if head, err = s.Graph.CreateBaseline(ctx, "Repository", nil); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		c, err := s.Graph.CreateChange(ctx, graph.NewChange{Namespace: domain.NamespacePlatform, Title: title, Intent: title, BaselineID: head.ID})
		if err != nil {
			return err
		}
		if _, err = s.Graph.AddItems(ctx, c.ID, items); err == nil {
			_, err = s.Graph.Apply(ctx, c.ID, title)
		}
		if errors.Is(err, graph.ErrConflict) && attempt < 3 {
			continue // main moved: read again and rebuild
		}
		return err
	}
}

func (s *GraphStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func create(typ, k string, props map[string]any) domain.ChangeItem {
	return domain.ChangeItem{ID: domain.ItemID(uuid.NewString()), Kind: domain.KindProposal, Type: "registry", ProducedBy: "registrysvc",
		Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: k, Type: typ, Properties: props}}}
}

func update(n domain.Node, props map[string]any) domain.ChangeItem {
	ref := n.Ref()
	return domain.ChangeItem{Kind: domain.KindProposal, Type: "registry", ProducedBy: "registrysvc",
		Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: props}}}
}

func markDeleted(n domain.Node) domain.ChangeItem {
	return update(n, map[string]any{"status": string(statusDeleted)})
}

// ---- methodologies ---------------------------------------------------------

func (s *GraphStore) Save(ctx context.Context, r Record) error {
	m := r.Methodology
	k := key(m.Name, m.Version)
	return s.commit(ctx, "Methodology "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, exists := d.methodologies[k]
		revive := exists && old.rec.Status == statusDeleted
		if exists && !revive && old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		created := r.UpdatedAt
		if exists && !revive {
			created = old.rec.CreatedAt
		}
		props, err := versionProps(m, r.Status, created, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		if err != nil {
			return nil, err
		}
		if exists {
			return []domain.ChangeItem{update(old.node, props)}, nil
		}
		return []domain.ChangeItem{create(TypeMethodologyVersion, MethodologyVersionKey(m.Name, m.Version), props)}, nil
	})
}

func (s *GraphStore) Get(ctx context.Context, name, version string) (Record, error) {
	d, err := s.read(ctx)
	if err != nil {
		return Record{}, err
	}
	if version != "" {
		r, ok := d.methodologies[key(name, version)]
		if !ok || r.rec.Status == statusDeleted {
			return Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
		}
		return r.rec, nil
	}
	var best Record
	found := false
	for _, r := range d.methodologies {
		if r.rec.Methodology.Name == name && r.rec.Status == StatusPublished && (!found || r.rec.PublishedAt.After(best.PublishedAt)) {
			best, found = r.rec, true
		}
	}
	if !found {
		return Record{}, fmt.Errorf("%s (published): %w", name, ErrNotFound)
	}
	return best, nil
}

func (s *GraphStore) List(ctx context.Context) ([]Record, error) {
	d, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(d.methodologies))
	for _, r := range d.methodologies {
		if r.rec.Status != statusDeleted {
			out = append(out, r.rec)
		}
	}
	sortRecords(out)
	return out, nil
}

func (s *GraphStore) SetStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	k := key(name, version)
	return s.commit(ctx, fmt.Sprintf("Methodology %s %s", k, st), func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		r := old.rec
		r.Status, r.UpdatedAt = st, at
		if st == StatusPublished {
			r.PublishedAt = at
		}
		props, err := versionProps(r.Methodology, r.Status, r.CreatedAt, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		if err != nil {
			return nil, err
		}
		return []domain.ChangeItem{update(old.node, props)}, nil
	})
}

func (s *GraphStore) Delete(ctx context.Context, name, version string) error {
	k := key(name, version)
	return s.commit(ctx, "Delete methodology "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.methodologies[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, ErrNotFound)
		}
		if old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("%s: %w", k, ErrImmutable)
		}
		return []domain.ChangeItem{markDeleted(old.node)}, nil
	})
}

// ---- domains -----------------------------------------------------------------

func (s *GraphStore) SaveDomain(ctx context.Context, r DomainRecord) error {
	dm := r.Domain
	k := key(dm.Name, dm.Version)
	return s.commit(ctx, "Domain "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, exists := d.domains[k]
		revive := exists && old.rec.Status == statusDeleted
		if exists && !revive && old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("domain %s: %w", k, ErrImmutable)
		}
		created := r.UpdatedAt
		if exists && !revive {
			created = old.rec.CreatedAt
		}
		props, err := versionProps(dm, r.Status, created, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		if err != nil {
			return nil, err
		}
		if exists {
			return []domain.ChangeItem{update(old.node, props)}, nil
		}
		return []domain.ChangeItem{create(TypeDomainVersion, DomainVersionKey(dm.Name, dm.Version), props)}, nil
	})
}

func (s *GraphStore) GetDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	d, err := s.read(ctx)
	if err != nil {
		return DomainRecord{}, err
	}
	if version != "" {
		r, ok := d.domains[key(name, version)]
		if !ok || r.rec.Status == statusDeleted {
			return DomainRecord{}, fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
		}
		return r.rec, nil
	}
	var best DomainRecord
	found := false
	for _, r := range d.domains {
		if r.rec.Domain.Name == name && r.rec.Status == StatusPublished && (!found || r.rec.PublishedAt.After(best.PublishedAt)) {
			best, found = r.rec, true
		}
	}
	if !found {
		return DomainRecord{}, fmt.Errorf("%s (published): %w", name, errDomainNotFound)
	}
	return best, nil
}

func (s *GraphStore) ListDomains(ctx context.Context) ([]DomainRecord, error) {
	d, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DomainRecord, 0, len(d.domains))
	for _, r := range d.domains {
		if r.rec.Status != statusDeleted {
			out = append(out, r.rec)
		}
	}
	sortDomainRecords(out)
	return out, nil
}

func (s *GraphStore) SetDomainStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	k := key(name, version)
	return s.commit(ctx, fmt.Sprintf("Domain %s %s", k, st), func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.domains[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, errDomainNotFound)
		}
		r := old.rec
		r.Status, r.UpdatedAt = st, at
		if st == StatusPublished {
			r.PublishedAt = at
		}
		props, err := versionProps(r.Domain, r.Status, r.CreatedAt, r.UpdatedAt, r.PublishedAt, r.UpdatedBy)
		if err != nil {
			return nil, err
		}
		return []domain.ChangeItem{update(old.node, props)}, nil
	})
}

func (s *GraphStore) DeleteDomain(ctx context.Context, name, version string) error {
	k := key(name, version)
	return s.commit(ctx, "Delete domain "+k, func(d *defs) ([]domain.ChangeItem, error) {
		old, ok := d.domains[k]
		if !ok || old.rec.Status == statusDeleted {
			return nil, fmt.Errorf("%s: %w", k, errDomainNotFound)
		}
		if old.rec.Status != StatusDraft {
			return nil, fmt.Errorf("domain %s: %w", k, ErrImmutable)
		}
		return []domain.ChangeItem{markDeleted(old.node)}, nil
	})
}
