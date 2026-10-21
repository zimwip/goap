package gitimport

import (
	"strings"
	"testing"
)

func TestDedent(t *testing.T) {
	src := []byte("class A:\n    def f(self):\n        return 1\n")
	start := uint(strings.Index(string(src), "def"))
	if got, want := dedent(leadOf(src, start)+"def f(self):\n        return 1"), "def f(self):\n    return 1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := leadOf([]byte("x = 1; def"), 7); got != "" {
		t.Errorf("lead %q", got)
	}
	if got, want := dedent("\tfunc f() {\n\n\t\tx()\n\t}"), "func f() {\n\n\tx()\n}"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
