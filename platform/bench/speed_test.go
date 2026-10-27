package bench

import (
	"math"
	"testing"
	"time"
)

func TestSpeedDrift(t *testing.T) {
	cases := []struct {
		name string
		// at is the time the run reaches v versions
		at    func(v float64) float64
		drift float64
	}{
		{"steady", func(v float64) float64 { return v / 100 }, 0},
		// a write costs as much as the versions already written: twice the data, half the speed
		{"linear cost", func(v float64) float64 { return v * v / 2e6 }, -0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Speed
			s.Add(0, 0)
			s.Add(5*time.Second, 0) // waiting for the graph: the writes start here
			for v := 100.0; v <= 20000; v += 100 {
				s.Add(5*time.Second+time.Duration(c.at(v)*float64(time.Second)), int64(v))
			}
			d, ok := s.Drift()
			if !ok || math.Abs(d-c.drift) > 0.02 {
				t.Fatalf("drift %v %v, want %v", d, ok, c.drift)
			}
			if r, ok := s.Rate(); !ok || r <= 0 {
				t.Fatalf("rate %v %v", r, ok)
			}
		})
	}
	var s Speed
	s.Add(0, 0)
	s.Add(time.Second, 50)
	if _, ok := s.Drift(); ok {
		t.Fatal("a drift measured over too few versions")
	}
}
