package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// The log of a change (ADR 0030) holds its facts, its journal records and the events of its change impacts: the
// repositories store entries (Tx.AppendLog, Tx.Log); these helpers write and read the typed values through them.

func putItem(ctx context.Context, tx Tx, change domain.ChangeID, it domain.ChangeItem) error {
	e, err := domain.FactEntry(change, it)
	if err != nil {
		return err
	}
	_, err = tx.AppendLog(ctx, e)
	return err
}

func appendImpactEvent(ctx context.Context, tx Tx, ev domain.ImpactEvent) (domain.ImpactEvent, error) {
	e, err := domain.ImpactEntry(ev)
	if err != nil {
		return ev, err
	}
	if e, err = tx.AppendLog(ctx, e); err != nil {
		return ev, err
	}
	ev.Seq = int(e.Seq)
	return ev, nil
}

// impactEvents is the impact log of a change (ADR 0029).
func impactEvents(ctx context.Context, tx Tx, change domain.ChangeID) ([]domain.ImpactEvent, error) {
	entries, err := tx.Log(ctx, domain.LogFilter{Change: change, Types: []string{domain.LogImpact + "."}})
	if err != nil {
		return nil, err
	}
	return decodeImpacts(ctx, tx, entries)
}

// decodeImpacts reads impact events from their log entries (domain.ImpactEventsOf: one, or the ones an entry carries), the drafts that copy a version as is
// rebuilt from it (ADR 0142).
func decodeImpacts(ctx context.Context, tx Tx, entries []domain.LogEntry) ([]domain.ImpactEvent, error) {
	var out []domain.ImpactEvent // nil when empty, as the stores returned before
	var bases []domain.NodeRef
	seen := map[domain.NodeRef]bool{} // a version several events copy is read once: its links would come back once per copy
	for _, l := range entries {
		events, err := domain.ImpactEventsOf(l)
		if err != nil {
			return nil, fmt.Errorf("log entry %d (%s): %w", l.Seq, l.Type, err)
		}
		for _, e := range events {
			if !e.Hydrated() && e.Draft.Base != nil && !seen[*e.Draft.Base] {
				seen[*e.Draft.Base] = true
				bases = append(bases, *e.Draft.Base)
			}
			out = append(out, e)
		}
	}
	if len(bases) == 0 {
		return out, nil
	}
	nodes, err := tx.NodesAt(ctx, bases, "")
	if err != nil {
		return nil, err
	}
	byRef := make(map[domain.NodeRef]domain.Node, len(nodes))
	for _, n := range nodes {
		byRef[n.Ref()] = n
	}
	links, err := tx.LinksFrom(ctx, bases)
	if err != nil {
		return nil, err
	}
	from := map[domain.NodeRef][]domain.Link{}
	for _, l := range links {
		from[l.From] = append(from[l.From], l)
	}
	for i, e := range out {
		if e.Hydrated() || e.Draft.Base == nil {
			continue
		}
		base, ok := byRef[*e.Draft.Base]
		if !ok {
			return nil, fmt.Errorf("impact event %s copies version %s, which is not stored", e.ID, *e.Draft.Base)
		}
		d := domain.CopyDraft(e, base, from[base.Ref()])
		out[i].Draft = &d
	}
	return out, nil
}

// itemsOf decodes the facts of a log.
func itemsOf(entries []domain.LogEntry) ([]domain.ChangeItem, error) {
	return decodeLog[domain.ChangeItem](entries, nil)
}

func decodeLog[T any](entries []domain.LogEntry, set func(*T, domain.LogEntry)) ([]T, error) {
	var out []T // nil when empty, as the stores returned before
	for _, l := range entries {
		var v T
		if err := json.Unmarshal(l.Payload, &v); err != nil {
			return nil, fmt.Errorf("log entry %d (%s): %w", l.Seq, l.Type, err)
		}
		if set != nil {
			set(&v, l)
		}
		out = append(out, v)
	}
	return out, nil
}

// factsFilter selects the facts of a change.
func factsFilter(change domain.ChangeID) domain.LogFilter {
	return domain.LogFilter{Change: change, Types: []string{domain.LogFact + "."}}
}

// logWhere is the WHERE clause of a log filter for the SQL repositories (the limit aside); ph gives the placeholder
// of the n-th argument (1-based).
func logWhere(f domain.LogFilter, ph func(int) string) (string, []any) {
	var conds []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return ph(len(args))
	}
	in := func(col string, vals []string) {
		ps := make([]string, len(vals))
		for i, v := range vals {
			ps[i] = arg(v)
		}
		conds = append(conds, col+" IN ("+strings.Join(ps, ", ")+")")
	}
	if f.Change != "" {
		conds = append(conds, "change_id = "+arg(string(f.Change)))
	}
	if len(f.Types) > 0 {
		var or []string
		for _, t := range f.Types {
			if strings.HasSuffix(t, ".") {
				or = append(or, "type LIKE "+arg(t+"%"))
			} else {
				or = append(or, "type = "+arg(t))
			}
		}
		conds = append(conds, "("+strings.Join(or, " OR ")+")")
	}
	if f.Flows != nil {
		if len(f.Flows) == 0 {
			conds = append(conds, "1 = 0")
		} else {
			in("flow", f.Flows)
		}
	}
	if len(f.Processes) > 0 {
		in("process_id", f.Processes)
	}
	if f.Execution != "" {
		conds = append(conds, "execution = "+arg(f.Execution))
	}
	if f.AfterSeq > 0 {
		conds = append(conds, "seq > "+arg(f.AfterSeq))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ChangeLog returns the entries of a change's log matching f (ADR 0030), and the number of entries of each type
// matching f without its types (what each type would add); f.Change or f.Processes is required (a process names its
// change).
func (g *Graph) ChangeLog(ctx context.Context, f domain.LogFilter) (out []domain.LogEntry, counts map[string]int, err error) {
	if f.Change == "" && len(f.Processes) == 0 {
		return nil, nil, fmt.Errorf("the log of which change: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if f.Change != "" {
			if _, err := tx.Change(ctx, f.Change); err != nil {
				return err
			}
		}
		if out, err = tx.Log(ctx, f); err != nil {
			return err
		}
		all := f
		all.Types = nil
		counts, err = tx.LogCounts(ctx, all)
		return err
	})
	return
}

// AppendLog appends entries to the logs of their changes, as one write (ids are assigned and the time set when empty).
// The graph does not look into them: what an entry says is its stream's, a use case (the execution journal,
// pkg/journal). The streams the graph writes itself (domain.LogFact, domain.LogImpact: items and change impacts have
// their own operations, which check them) are refused, and so is an entry of an unknown change.
func (g *Graph) AppendLog(ctx context.Context, entries []domain.LogEntry) error {
	for _, e := range entries {
		if e.Change == "" || !strings.Contains(e.Type, ".") {
			return fmt.Errorf("a log entry needs a change and a type <stream>.<kind>: %w", ErrInvalid)
		}
		if s := e.Stream(); s == domain.LogFact || s == domain.LogImpact || s == domain.LogChange {
			return fmt.Errorf("the %s stream of a log is written by the graph only: %w", s, ErrInvalid)
		}
	}
	return g.repo.InTx(ctx, func(tx Tx) error {
		for _, e := range entries {
			if e.ID == "" {
				e.ID = g.newID()
			}
			if e.At.IsZero() {
				e.At = g.now()
			}
			if _, err := tx.AppendLog(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}
