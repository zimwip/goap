package index

import (
	"context"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/zimwip/goap/pkg/domain"
)

// Memory is an in-memory Store (tests, and goap-dev without a database). Its full text is a token
// match ranked by term frequency; vectors are scanned exactly.
type Memory struct {
	mu   sync.Mutex
	docs map[Ref]*Doc
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{docs: map[Ref]*Doc{}} }

func (m *Memory) Upsert(_ context.Context, d Doc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.docs[d.Ref()]; ok {
		d.Main = d.Main || old.Main
		if d.Embedding == nil && old.Hash == d.Hash {
			d.Embedding = old.Embedding
		}
	}
	d.Kind = kindOf(d.Kind)
	m.docs[d.Ref()] = &d
	return nil
}

// Ref identifies the document.
func (d Doc) Ref() Ref { return Ref{Kind: kindOf(d.Kind), ID: d.ID, Version: d.Version} }

func (m *Memory) Hash(_ context.Context, kind string, id domain.NodeID, v domain.Version) (string, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.docs[Ref{Kind: kindOf(kind), ID: id, Version: v}]
	if !ok {
		return "", false, false, nil
	}
	return d.Hash, d.Embedding != nil, true, nil
}

func (m *Memory) Delete(_ context.Context, kind string, id domain.NodeID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ref := range m.docs {
		if ref.Kind == kindOf(kind) && ref.ID == id {
			delete(m.docs, ref)
		}
	}
	return nil
}

func (m *Memory) Texts(_ context.Context, refs []Ref) (map[Ref]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[Ref]string, len(refs))
	for _, r := range refs {
		if d, ok := m.docs[r]; ok {
			out[r] = d.Text
		}
	}
	return out, nil
}

func (m *Memory) Vector(_ context.Context, kind string, id domain.NodeID) (Hit, []float32, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Doc
	for ref, d := range m.docs {
		if ref.Kind != kindOf(kind) || ref.ID != id {
			continue
		}
		if best == nil || (d.Main && !best.Main) || (d.Main == best.Main && d.Version > best.Version) {
			best = d
		}
	}
	if best == nil {
		return Hit{}, nil, false, nil
	}
	return best.hit(), best.Embedding, true, nil
}

func (m *Memory) SetMain(_ context.Context, set map[domain.NodeID]domain.Version, removed []domain.NodeID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gone := map[domain.NodeID]bool{}
	for _, id := range removed {
		gone[id] = true
	}
	for ref, d := range m.docs {
		if ref.Kind != KindNode {
			continue
		}
		if v, ok := set[ref.ID]; ok {
			d.Main = v == ref.Version
		} else if gone[ref.ID] {
			d.Main = false
		}
	}
	return nil
}

func (m *Memory) Reset(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs = map[Ref]*Doc{}
	return nil
}

func (d *Doc) hit() Hit {
	return Hit{Kind: kindOf(d.Kind), ID: d.ID, Version: d.Version, Namespace: d.Namespace, Type: d.Type, Key: d.Key, State: d.State, Branch: d.Branch,
		Main: d.Main, Deleted: d.Deleted, Project: d.Project, Owner: d.Owner, Status: d.Status, Methodology: d.Methodology, Parent: d.Parent,
		PersonalTo: d.PersonalTo, Title: d.Title, Facets: d.Facets}
}

func (m *Memory) scan(f Filter, score func(*Doc) (float64, bool), limit int) []Hit {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Hit
	for _, d := range m.docs {
		h := d.hit()
		if !f.matches(h) {
			continue
		}
		if s, ok := score(d); ok {
			h.Score = s
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Version < out[j].Version
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (m *Memory) List(_ context.Context, f Filter, limit int) ([]Hit, error) {
	return m.scan(f, func(*Doc) (float64, bool) { return 0, true }, limit), nil
}

func (m *Memory) FullText(_ context.Context, text string, f Filter, limit int) ([]Hit, error) {
	terms := Tokens(text)
	return m.scan(f, func(d *Doc) (float64, bool) {
		toks := Tokens(d.Key + " " + d.Text)
		score := 0.0
		for _, t := range terms {
			n := 0
			for _, x := range toks {
				if strings.HasPrefix(x, t) {
					n++
				}
			}
			if n == 0 {
				return 0, false // every term must match
			}
			score += float64(n)
		}
		return score, len(terms) > 0
	}, limit), nil
}

func (m *Memory) Nearest(_ context.Context, vec []float32, f Filter, limit int) ([]Hit, error) {
	return m.scan(f, func(d *Doc) (float64, bool) {
		if d.Embedding == nil {
			return 0, false
		}
		return cosine(vec, d.Embedding), true
	}, limit), nil
}

// Tokens lowercases and splits a text into alphanumeric terms.
func Tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
