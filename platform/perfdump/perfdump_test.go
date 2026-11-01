package perfdump

import (
	"strings"
	"testing"
	"time"
)

func TestLastRunAndRows(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	s := Samples{}
	// an earlier run, then a new one starting from zero
	for i := 0; i < 5; i++ {
		s[Volume] = append(s[Volume], Point{t0.Add(time.Duration(i) * 15 * time.Second), float64(1000 * (i + 1))})
	}
	start := t0.Add(5 * 15 * time.Second)
	for i := 0; i < 40; i++ {
		at := start.Add(time.Duration(i) * 15 * time.Second)
		s[Volume] = append(s[Volume], Point{at, float64(500 * (i + 1))})
		s["versions/s"] = append(s["versions/s"], Point{at, float64(1000 - i*10)})
		s["changes"] = append(s["changes"], Point{at, float64(50 * (i + 1))})
	}
	from, to, ok := LastRun(s)
	if !ok || !from.Equal(start) || !to.Equal(start.Add(39*15*time.Second)) {
		t.Fatalf("last run %v %v %v", from, to, ok)
	}
	rows := Rows(s, from, to, 4)
	if len(rows) != 4 || rows[3].Volume != 20000 || rows[0].Values["changes"] != 500 {
		t.Fatalf("rows %+v", rows)
	}
	if v := rows[0].Values["versions/s"]; v != 955 { // mean of 1000..910
		t.Fatalf("mean rate %v", v)
	}
	var b strings.Builder
	if err := Write(&b, Header{At: t0, Commit: "abc1234", Args: "-mode lifecycle", From: from, To: to}, s, rows); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"commit: abc1234", "bench args: -mode lifecycle", "reached: 20000 versions", "| 5000 |", "| versions |"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

func TestNiceStep(t *testing.T) {
	for x, want := range map[float64]float64{13800: 20000, 9000: 10000, 4100: 5000, 1: 1} {
		if got := niceStep(x); got != want {
			t.Fatalf("niceStep(%v) = %v, want %v", x, got, want)
		}
	}
}
