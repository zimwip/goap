package bench

import (
	"sort"
	"sync"
	"time"
)

// Speed follows how fast a run writes as its data grows: the versions written per second over the last tenth of the
// versions written (Speed.Rate), and its drift (Speed.Drift), that speed against the speed over the same tenth of half
// the volume: 0 when the speed holds as the data doubles, -0.25 when it loses a quarter at each doubling, so a run whose
// writes slow down only with the logarithm of the volume (an index a level deeper) keeps a drift near 0, one whose cost
// grows with the volume (a read of a whole table, a list that grows) a drift heading for -0.5.
type Speed struct {
	mu  sync.Mutex
	pts []speedPoint // in the order of the run, the versions increasing
}

type speedPoint struct {
	at time.Duration
	v  int64
}

// speedSpan is the share of the volume each speed is measured over, speedMin the fewest versions it is measured over.
const (
	speedSpan = 0.1
	speedMin  = 100
)

// Add records the versions written at a time of the run (called every second, and at the end).
func (s *Speed) Add(at time.Duration, v int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.pts)
	switch {
	case n > 0 && v < s.pts[n-1].v:
		return
	case n > 0 && v == s.pts[n-1].v:
		if v == 0 {
			s.pts[n-1].at = at // the writes start when the last empty point was taken
		}
		return
	}
	s.pts = append(s.pts, speedPoint{at, v})
}

// timeAt is when the run reached v versions, between two points; false before the first point or past the last.
func (s *Speed) timeAt(v float64) (time.Duration, bool) {
	i := sort.Search(len(s.pts), func(i int) bool { return float64(s.pts[i].v) >= v })
	if i == len(s.pts) || (i == 0 && float64(s.pts[0].v) > v) {
		return 0, false
	}
	if i == 0 || float64(s.pts[i].v) == v {
		return s.pts[i].at, true
	}
	a, b := s.pts[i-1], s.pts[i]
	f := (v - float64(a.v)) / float64(b.v-a.v)
	return a.at + time.Duration(f*float64(b.at-a.at)), true
}

// rate is the versions per second between lo and hi versions.
func (s *Speed) rate(lo, hi float64) (float64, bool) {
	if hi-lo < speedMin {
		return 0, false
	}
	t0, ok0 := s.timeAt(lo)
	t1, ok1 := s.timeAt(hi)
	if !ok0 || !ok1 || t1 <= t0 {
		return 0, false
	}
	return (hi - lo) / (t1 - t0).Seconds(), true
}

// Rate is the versions written per second over the last tenth of the versions written; false while too few are.
func (s *Speed) Rate() (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pts) == 0 {
		return 0, false
	}
	v := float64(s.pts[len(s.pts)-1].v)
	return s.rate((1-speedSpan)*v, v)
}

// Drift is the relative change of the speed when the volume doubles: the rate over the last tenth of the versions
// written against the rate over the same tenth of half of them, minus 1 (-0.2: a fifth slower at twice the volume);
// false while either is not measured.
func (s *Speed) Drift() (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pts) == 0 {
		return 0, false
	}
	v := float64(s.pts[len(s.pts)-1].v)
	now, ok := s.rate((1-speedSpan)*v, v)
	if !ok {
		return 0, false
	}
	half, ok := s.rate((1-speedSpan)*v/2, v/2)
	if !ok || half <= 0 {
		return 0, false
	}
	return now/half - 1, true
}
