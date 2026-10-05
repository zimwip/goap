package domain_test

import (
	"reflect"
	"testing"

	. "github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/typecat"
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

// The hierarchy of a structure keeps the links the structure names between nodes of its types (subtypes included).
func TestStructuresHierarchy(t *testing.T) {
	st := typecat.Builtin().Structures()
	org, proj := st.Organisation(), st.Project()
	user := st.TypesOf(StructureOrganisation)[len(st.TypesOf(StructureOrganisation))-1] // a User is a unit: a subtype
	node := func(id, typ string) Node {
		return Node{ID: NodeID(id), Namespace: org.Namespace, Key: id, Type: typ}
	}
	nodes := []Node{node(org.Root, org.Type), node("ORG-A", org.Type), node("USR:x", user), node(proj.Root, proj.Type)}
	byKey := map[string]Node{}
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	link := func(typ, from, to string) Link { return Link{Type: typ, From: byKey[from].Ref(), To: byKey[to].Ref()} }
	links := []Link{link(org.Parent, "ORG-A", org.Root), link("organisation@member_of", "USR:x", "ORG-A"), link(proj.Parent, proj.Root, proj.Root)}
	h := st.Hierarchy(StructureOrganisation, nodes, links)
	if got, want := h.Chain("ORG-A"), []string{"ORG-A", org.Root}; !reflect.DeepEqual(got, want) {
		t.Errorf("unit chain %v, want %v", got, want)
	}
	if p := h.Parent("USR:x"); p != "" {
		t.Errorf("member_of is not part_of: %q", p)
	}
	if got := st.Hierarchy(StructureProject, nodes, links).Chain(proj.Root); !reflect.DeepEqual(got, []string{proj.Root}) {
		t.Errorf("project chain %v", got)
	}
}
