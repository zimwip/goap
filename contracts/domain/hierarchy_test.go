package domain_test

import (
	"reflect"
	"testing"

	. "github.com/zimwip/goap/contracts/domain"
)

func TestHierarchyChain(t *testing.T) {
	h := NewHierarchy("ROOT")
	h.Add("C", "B")
	h.Add("B", "A")
	h.Add("A", "A") // a self link is no parent
	h.Add("X", "Y") // a cycle stops the walk
	h.Add("Y", "X")
	for key, want := range map[string][]string{
		"C":    {"C", "B", "A", "ROOT"},
		"A":    {"A", "ROOT"},
		"":     {"ROOT"},
		"ROOT": {"ROOT"},
		"X":    {"X", "Y", "ROOT"},
		"new":  {"new", "ROOT"},
	} {
		if got := h.Chain(key); !reflect.DeepEqual(got, want) {
			t.Errorf("Chain(%q) = %v, want %v", key, got, want)
		}
	}
}
