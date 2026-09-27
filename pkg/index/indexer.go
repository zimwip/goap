package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Indexer keeps a store in step with the events of the graph.
type Indexer struct {
	Store    Store
	Embedder Embedder // nil: text-only index
	// EmbedFailed is told when a document could not be embedded. It is then indexed as text only, with an
	// empty hash so that the next delivery or a reindex embeds it again.
	EmbedFailed func(key string, err error)
}

// OnNode indexes a written node version. Deleted versions are indexed too (flagged), so they can be
// excluded by the main facet and stay searchable in the history of a branch.
func (x *Indexer) OnNode(ctx context.Context, ev domain.NodeEvent) error {
	d := Doc{ID: ev.ID, Version: ev.Version, Namespace: ev.Namespace, Type: ev.Type, Key: ev.Key, State: ev.State, Branch: ev.Branch,
		Deleted: ev.Deleted, Text: DocumentText(ev.Key, ev.Text), Time: ev.Time, Facets: facetStrings(ev.Facets)}
	sum := sha256.Sum256([]byte(d.Text))
	d.Hash = hex.EncodeToString(sum[:])
	if hash, embedded, found, err := x.Store.Hash(ctx, d.ID, d.Version); err != nil {
		return err
	} else if x.Embedder != nil && !(found && embedded && hash == d.Hash) {
		if vecs, err := x.Embedder.Embed(ctx, []string{d.Text}); err != nil {
			d.Hash = ""
			if x.EmbedFailed != nil {
				x.EmbedFailed(d.Key, err)
			}
		} else if len(vecs) == 1 {
			d.Embedding = vecs[0]
		}
	}
	return x.Store.Upsert(ctx, d)
}

// OnBaseline follows the head of main: only its diffs matter to the main facet.
func (x *Indexer) OnBaseline(ctx context.Context, ev domain.BaselineEvent) error {
	if domain.BranchOf(ev.Branch) != domain.MainBranch {
		return nil
	}
	return x.Store.SetMain(ctx, ev.Set, ev.Removed)
}

// DocumentText builds the text of a node: its key then its searchable properties, in a stable order.
func DocumentText(key string, text map[string]string) string {
	var b strings.Builder
	b.WriteString(key)
	names := make([]string, 0, len(text))
	for k := range text {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		b.WriteString("\n" + k + ": " + text[k])
	}
	return b.String()
}

func facetStrings(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = fmt.Sprint(v)
	}
	return out
}
