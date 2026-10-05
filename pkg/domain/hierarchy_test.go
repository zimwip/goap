package domain

import (
	"reflect"
	"testing"
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
	st := BuiltinStructureSet()
	node := func(id, typ string) Node {
		return Node{ID: NodeID(id), Namespace: NamespaceOrganisation, Key: id, Type: typ}
	}
	nodes := []Node{node("ORG-DEFAULT", TypeOrgUnit), node("ORG-A", TypeOrgUnit), node("USR:x", TypeUser), node("PROJ-ROOT", TypeProjectUnit)}
	byKey := map[string]Node{}
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	link := func(typ, from, to string) Link { return Link{Type: typ, From: byKey[from].Ref(), To: byKey[to].Ref()} }
	links := []Link{link(LinkPartOf, "ORG-A", "ORG-DEFAULT"), link(LinkMemberOf, "USR:x", "ORG-A"), link(LinkProjectPartOf, "PROJ-ROOT", "PROJ-ROOT")}
	h := st.Hierarchy(StructureOrganisation, nodes, links)
	if got, want := h.Chain("ORG-A"), []string{"ORG-A", "ORG-DEFAULT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unit chain %v, want %v", got, want)
	}
	if p := h.Parent("USR:x"); p != "" {
		t.Errorf("member_of is not part_of: %q", p)
	}
	if got := st.Hierarchy(StructureProject, nodes, links).Chain("PROJ-ROOT"); !reflect.DeepEqual(got, []string{"PROJ-ROOT"}) {
		t.Errorf("project chain %v", got)
	}
}
