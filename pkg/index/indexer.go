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
	return x.put(ctx, Doc{Kind: KindNode, ID: ev.ID, Version: ev.Version, Namespace: ev.Namespace, Type: ev.Type, Key: ev.Key, State: ev.State, Branch: ev.Branch,
		Deleted: ev.Deleted, Project: ev.Project, Owner: ev.Owner, Text: DocumentText(ev.Key, ev.Text), Time: ev.Time, Facets: facetStrings(ev.Facets)})
}

// Bounds of the text of a change document (ADR 0095): the title, the intent and the goal are short by nature, and a
// change acting on many nodes gives their keys up to the cap. The items, decisions, reviews and drafts are not indexed.
const (
	MaxChangeField = 2000
	MaxChangeText  = 4000
)

// OnChange indexes the document of a change, or drops it when the change was purged. personalTo is the subject a
// personal change belongs to, decided by the caller (the index knows no organisation).
func (x *Indexer) OnChange(ctx context.Context, ev domain.ChangeDocEvent, personalTo string) error {
	if ev.Deleted {
		return x.Store.Delete(ctx, KindChange, domain.NodeID(ev.ID))
	}
	branch := ev.Branch
	if branch == "" {
		branch = domain.MainBranch
	}
	return x.put(ctx, Doc{Kind: KindChange, ID: domain.NodeID(ev.ID), Namespace: ev.Namespace, Key: string(ev.ID), State: ev.State, Branch: branch,
		Main: true, Project: ev.ProjectID, Owner: ev.OwnerOrg, Status: string(ev.Status), Methodology: ev.Methodology, Parent: string(ev.ParentID),
		PersonalTo: personalTo, Title: ev.Title, Text: ChangeText(ev), Time: ev.CreatedAt})
}

// ChangeText builds the text of a change: its title, intent and goal, its methodology and namespace, then the keys
// (and types) of the nodes it acts on, bounded.
func ChangeText(ev domain.ChangeDocEvent) string {
	var b strings.Builder
	b.WriteString(string(ev.ID))
	add := func(name, v string) {
		if v != "" {
			b.WriteString("\n" + name + ": " + truncate(v, MaxChangeField))
		}
	}
	add("title", ev.Title)
	add("intent", ev.Intent)
	add("goal", ev.Goal)
	add("methodology", ev.Methodology)
	add("namespace", ev.Namespace)
	if len(ev.Impacts) > 0 {
		parts := make([]string, len(ev.Impacts))
		for i, r := range ev.Impacts {
			parts[i] = r.Key
			if r.Type != "" {
				parts[i] += " (" + r.Type + ")"
			}
		}
		b.WriteString("\nnodes: " + strings.Join(parts, ", "))
	}
	return truncate(b.String(), MaxChangeText)
}

func truncate(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// put embeds the document unless its text is unchanged and already embedded, then stores it.
func (x *Indexer) put(ctx context.Context, d Doc) error {
	sum := sha256.Sum256([]byte(d.Text))
	d.Hash = hex.EncodeToString(sum[:])
	if hash, embedded, found, err := x.Store.Hash(ctx, d.Kind, d.ID, d.Version); err != nil {
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
