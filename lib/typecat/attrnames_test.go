package typecat

import (
	"slices"
	"testing"
)

// A closed type's properties are its attributes, ancestors' included; `additionalProperties` opens a type and its
// subtypes; a type the catalogue does not know is open (strict attributes).
func TestAttributeNames(t *testing.T) {
	d := parse(t, `
name: shop
version: 1.0.0
nodeTypes:
  - {name: Item, attributes: [{name: title, type: string}]}
  - {name: Book, extends: Item, attributes: [{name: isbn, type: string}]}
  - {name: Bag, additionalProperties: true, attributes: [{name: label, type: string}]}
  - {name: Pouch, extends: Bag}
`)
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatalf("valid: %v", issues)
	}
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	names, open := c.AttributeNames("shop@Book")
	if open || !slices.Equal(names, []string{"title", "isbn"}) {
		t.Errorf("Book: %v open=%v", names, open)
	}
	if _, open := c.AttributeNames("shop@Bag"); !open {
		t.Error("Bag should be open")
	}
	if names, open := c.AttributeNames("shop@Pouch"); !open || !slices.Equal(names, []string{"label"}) {
		t.Errorf("Pouch inherits the opening: %v open=%v", names, open)
	}
	if _, open := c.AttributeNames("shop@Nope"); !open {
		t.Error("an unknown type is open")
	}
}
