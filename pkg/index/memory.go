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
	docs map[domain.NodeRef]*Doc
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{docs: map[domain.NodeRef]*Doc{}} }

func (m *Memory) Upsert(_ context.Context, d Doc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.docs[d.Ref()]; ok {
		d.Main = d.Main || old.Main
		if d.Embedding == nil && old.Hash == d.Hash {
			d.Embedding = old.Embedding
		}
	}
	m.docs[d.Ref()] = &d
	return nil
}

// Ref is the node version of the document.
func (d Doc) Ref() domain.NodeRef { return domain.NodeRef{ID: d.ID, Version: d.Version} }

func (m *Memory) Hash(_ context.Context, id domain.NodeID, v domain.Version) (string, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.docs[domain.NodeRef{ID: id, Version: v}]
	if !ok {
		return "", false, false, nil
	}
	return d.Hash, d.Embedding != nil, true, nil
}

func (m *Memory) SetMain(_ context.Context, set map[domain.NodeID]domain.Version, removed []domain.NodeID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gone := map[domain.NodeID]bool{}
	for _, id := range removed {
		gone[id] = true
	}
	for ref, d := range m.docs {
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
	m.docs = map[domain.NodeRef]*Doc{}
	return nil
}

func (d *Doc) hit() Hit {
	return Hit{ID: d.ID, Version: d.Version, Namespace: d.Namespace, Type: d.Type, Key: d.Key, State: d.State, Branch: d.Branch, Main: d.Main, Facets: d.Facets}
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
