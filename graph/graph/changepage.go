package graph

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// ChangePage is a page of the changes a filter matches, newest first.
type ChangePage struct {
	Changes []domain.Change
	// Next is the cursor of the next (older) page; "": this page is the last.
	Next string
	// Total is the number of changes matched, every page together.
	Total int
}

// ChangesPage answers a page of ListChanges, newest first (creation time, then id), of at most limit changes (50 by
// default, at most 500) after cursor (the Next of the page before; "": the newest page). keep, when set, leaves out
// the changes the caller may not see before the page is cut, so a page is full whenever more remain; it is called
// outside the transaction.
func (g *Graph) ChangesPage(ctx context.Context, f ChangesFilter, limit int, cursor string, keep func(domain.Change) bool) (p ChangePage, err error) {
	limit = pageLimit(limit)
	f = g.scopeFilter(f)
	var heads []domain.Change
	err = g.repo.InTx(ctx, func(tx Tx) error {
		heads, err = tx.ChangeHeads(ctx, f)
		return err
	})
	if err != nil {
		return p, err
	}
	var matched []domain.Change
	for _, h := range heads {
		if f.Match(h) && (keep == nil || keep(h)) {
			matched = append(matched, h)
		}
	}
	slices.SortFunc(matched, func(a, b domain.Change) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(string(b.ID), string(a.ID))
	})
	p.Total = len(matched)
	start := 0
	if at, id, ok := parseChangeCursor(cursor); ok {
		start, _ = slices.BinarySearchFunc(matched, struct{}{}, func(c domain.Change, _ struct{}) int {
			// the changes up to the cursor, itself included, are before it
			if d := at.Compare(c.CreatedAt); d != 0 {
				return d
			}
			if string(c.ID) >= id {
				return -1
			}
			return 1
		})
	}
	page := matched[start:min(start+limit, len(matched))]
	if start+limit < len(matched) {
		p.Next = changeCursor(page[len(page)-1])
	}
	if f.HeadersOnly {
		p.Changes = slices.Clone(page)
		return p, nil
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		for _, h := range page {
			c, err := tx.Change(ctx, h.ID)
			if err != nil {
				return err
			}
			p.Changes = append(p.Changes, c)
		}
		return nil
	})
	return p, err
}

// changeCursor is the cursor of the page after c: its creation time and its id.
func changeCursor(c domain.Change) string {
	return c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + string(c.ID)
}

func parseChangeCursor(s string) (time.Time, string, bool) {
	at, id, ok := strings.Cut(s, "|")
	if !ok {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	return t, id, err == nil
}
