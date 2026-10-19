package bench

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestVaryKeepsTheMean(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	const draws = 200_000
	for _, c := range []struct {
		n, least int
		spread   float64
	}{{50, 1, 0.5}, {2, 0, 0.5}, {3, 1, 1}, {1, 1, 0.5}, {10, 1, 0}} {
		sum, lo, hi := 0, math.MaxInt, 0
		for range draws {
			k := varyCount(rnd, c.n, c.least, c.spread)
			sum, lo, hi = sum+k, min(lo, k), max(hi, k)
		}
		if mean := float64(sum) / draws; math.Abs(mean-float64(c.n)) > 0.01*float64(c.n)+0.01 || lo < c.least {
			t.Errorf("varyCount(%d, %d, %.1f): mean %.3f, min %d", c.n, c.least, c.spread, mean, lo)
		}
		if c.spread > 0 && c.n > c.least && lo == hi {
			t.Errorf("varyCount(%d, %d, %.1f) does not vary", c.n, c.least, c.spread)
		}
	}
	for _, p := range []float64{0.3, 0.9, 0, 1} {
		sum := 0.0
		for range draws {
			x := varyShare(rnd, p, 0.5)
			if x < 0 || x > 1 {
				t.Fatalf("varyShare(%.1f) = %f", p, x)
			}
			sum += x
		}
		if mean := sum / draws; math.Abs(mean-p) > 0.005 {
			t.Errorf("varyShare(%.1f): mean %.4f", p, mean)
		}
	}
}
