package typecat

import (
	"errors"
	"testing"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/risk"
	"github.com/zimwip/goap/contracts/verify"
	"github.com/zimwip/goap/lib/def"
)

// Every type of item the code knows (the change mechanism, the platform use cases) is declared by a built-in domain:
// a domain is where an item type lives, the code only gives it a meaning (ADR 0128).
func TestCodeItemTypesAreDeclared(t *testing.T) {
	risk.Register()
	assess.Register()
	verify.Register()
	c := Builtin()
	for _, typ := range append(domain.CoreItemTypes(), domain.RegisteredItemTypes()...) {
		x, ok := c.ItemType(string(typ))
		if !ok {
			t.Errorf("item type %s: no built-in domain declares it", typ)
			continue
		}
		if x.Description == "" || !IsBuiltin(x.Domain) {
			t.Errorf("item type %s: %+v", typ, x)
		}
	}
}

// An item type name is one across the domains in force.
func TestItemTypeNamesAreUnique(t *testing.T) {
	d := &def.Domain{Name: "itemtest", Version: "1.0.0", Schema: def.Schema{ItemTypes: []def.ItemType{{Name: "artifact"}}}}
	if _, err := New(d); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a name a built-in domain declares: %v", err)
	}
	d.ItemTypes[0].Name = "testResult"
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	if x, ok := c.ItemType("testResult"); !ok || x.Domain != "itemtest" || x.Version != "1.0.0" {
		t.Fatalf("declared: %+v", x)
	}
}
