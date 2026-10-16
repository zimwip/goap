package graph

import (
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/typecat"
)

// itemCatalog is the catalogue that declares the types of change item: the one in force, else (untyped graph) the
// built-in domains.
func (g *Graph) itemCatalog() TypeCatalog {
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			return c
		}
	}
	return builtinTypes()
}

// ItemTypes lists the types of change item the domains in force declare (ADR 0128), by name.
func (g *Graph) ItemTypes() []*typecat.ItemType { return g.itemCatalog().ItemTypes() }

// checkItem checks an item before the graph stores it: its structure and the validation of its use case
// (ChangeItem.Validate), then that a domain in force declares its type and that the keys of its data the type declares
// as attributes have their type and enum (ADR 0128); the other keys stay free.
func (g *Graph) checkItem(it domain.ChangeItem) error {
	if err := it.Validate(); err != nil {
		return err
	}
	checks, ok := g.itemCatalog().ItemAttributeChecks(string(it.Type))
	if !ok {
		return fmt.Errorf("unknown item type %q: no domain in force declares it", it.Type)
	}
	for _, a := range checks {
		if err := checkAttributeValue(a, it.Data[a.Name]); err != nil {
			return fmt.Errorf("data.%s: %v", a.Name, err)
		}
	}
	return nil
}
