package graph

import (
	"fmt"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A copy of a map or of a list of impacts writes its own shards and blocks: the one it was copied from keeps what it
// held.
func TestCopiesWriteTheirOwn(t *testing.T) {
	var m cowMap[domain.ChangeImpactID, int]
	var b impactBlocks
	for i := range 300 {
		id := domain.ChangeImpactID(fmt.Sprint("i", i))
		m.set(id, i)
		b.append(domain.ChangeImpact{ID: id})
	}
	m2, b2 := m.share(), b.share()
	m2.set("i1", -1)
	m2.del("i2")
	m2.set("new", 1)
	b2.set(1, domain.ChangeImpact{ID: "changed"})
	b2.append(domain.ChangeImpact{ID: "new"})
	if v, _ := m.get("i1"); v != 1 || !m.has("i2") || m.has("new") || m.len() != 300 {
		t.Fatalf("the map copied from changed: i1=%d len=%d", v, m.len())
	}
	if v, _ := m2.get("i1"); v != -1 || m2.has("i2") || !m2.has("new") || m2.len() != 300 {
		t.Fatalf("the copy: i1=%d len=%d", v, m2.len())
	}
	if b.at(1).ID != "i1" || b.n != 300 || b.list().Len() != 300 {
		t.Fatalf("the list copied from changed: %s %d", b.at(1).ID, b.n)
	}
	if b2.at(1).ID != "changed" || b2.at(300).ID != "new" || b2.list().Index("i299") != 299 {
		t.Fatalf("the copy: %s %s", b2.at(1).ID, b2.at(300).ID)
	}
	n := 0
	for range m2.all() {
		n++
	}
	if n != m2.len() {
		t.Fatalf("all: %d entries, len %d", n, m2.len())
	}
}
