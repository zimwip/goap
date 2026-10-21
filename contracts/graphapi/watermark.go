package graphapi

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
)

// A change is read as a view of its log (ADR 0152): the fold of its impact events and facts up to a position, its seq.
// A write appends to the log and folds what it appended into the view at once, so the caller gets back the position its
// write left the log at (its watermark) and may ask a later read to see at least that far (AtLeast): a read whose view
// is behind is refused with ErrBehind rather than answered with an older state.

// ErrBehind refuses a read whose view of a change does not reach the position asked for (AtLeast).
var ErrBehind = fmt.Errorf("the view of the change is behind the position asked for: %w", ErrConflict)

// Watermark records, per change, the last position of its log the writes of a call appended (WithWatermark).
type Watermark struct {
	mu  sync.Mutex
	seq map[domain.ChangeID]int64
}

type watermarkKey struct{}

// WithWatermark returns a context whose writes record the positions they leave each change's log at.
func WithWatermark(ctx context.Context) (context.Context, *Watermark) {
	w := &Watermark{}
	return context.WithValue(ctx, watermarkKey{}, w), w
}

// WatermarkOf is the watermark a context records the writes in, nil when none.
func WatermarkOf(ctx context.Context) *Watermark {
	w, _ := ctx.Value(watermarkKey{}).(*Watermark)
	return w
}

// Note records that the log of a change reached seq (a lower position is ignored).
func (w *Watermark) Note(id domain.ChangeID, seq int64) {
	if w == nil || seq <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seq == nil {
		w.seq = map[domain.ChangeID]int64{}
	}
	if seq > w.seq[id] {
		w.seq[id] = seq
	}
}

// Of is the position the writes left the log of a change at, 0 when they wrote none.
func (w *Watermark) Of(id domain.ChangeID) int64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq[id]
}

// All is the positions per change.
func (w *Watermark) All() map[domain.ChangeID]int64 {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.seq)
}

type atLeastKey struct{}

// AtLeast returns a context whose reads of a change must see its log up to seq at least (ErrBehind otherwise).
func AtLeast(ctx context.Context, id domain.ChangeID, seq int64) context.Context {
	if seq <= 0 {
		return ctx
	}
	m := maps.Clone(atLeast(ctx))
	if m == nil {
		m = map[domain.ChangeID]int64{}
	}
	if seq > m[id] {
		m[id] = seq
	}
	return context.WithValue(ctx, atLeastKey{}, m)
}

// AtLeastOf is the position a read of a change must reach, 0 when none is asked.
func AtLeastOf(ctx context.Context, id domain.ChangeID) int64 { return atLeast(ctx)[id] }

// AllAtLeast is every position asked for.
func AllAtLeast(ctx context.Context) map[domain.ChangeID]int64 { return maps.Clone(atLeast(ctx)) }

func atLeast(ctx context.Context) map[domain.ChangeID]int64 {
	m, _ := ctx.Value(atLeastKey{}).(map[domain.ChangeID]int64)
	return m
}

// Positions over RPC: the answer of a write carries its watermark in HeaderChangeSeq, a request the positions its reads
// must reach in HeaderAtLeast, both as "<change>:<seq>" pairs separated by commas.
const (
	HeaderChangeSeq = "X-Goap-Change-Seq"
	HeaderAtLeast   = "X-Goap-At-Least"
)

// FormatPositions writes positions as a header value.
func FormatPositions(m map[domain.ChangeID]int64) string {
	parts := make([]string, 0, len(m))
	for id, seq := range m {
		parts = append(parts, string(id)+":"+strconv.FormatInt(seq, 10))
	}
	return strings.Join(parts, ",")
}

// ParsePositions reads a header value; malformed pairs are left out.
func ParsePositions(v string) map[domain.ChangeID]int64 {
	var out map[domain.ChangeID]int64
	for _, p := range strings.Split(v, ",") {
		i := strings.LastIndexByte(p, ':')
		if i <= 0 {
			continue
		}
		seq, err := strconv.ParseInt(strings.TrimSpace(p[i+1:]), 10, 64)
		if err != nil || seq <= 0 {
			continue
		}
		if out == nil {
			out = map[domain.ChangeID]int64{}
		}
		out[domain.ChangeID(strings.TrimSpace(p[:i]))] = seq
	}
	return out
}
