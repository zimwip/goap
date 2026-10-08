package def

import (
	"os"
	"testing"
)

// The domains shipped with the code parse and validate (attributes, enums, algorithms and their plugs).
func TestShippedDomainsValidate(t *testing.T) {
	for _, f := range []string{"alm.yaml", "builtin/methodology.yaml", "builtin/organisation.yaml", "builtin/platform.yaml", "builtin/execution.yaml"} {
		b, err := os.ReadFile("../../../domains/" + f)
		if err != nil {
			t.Fatal(err)
		}
		d, err := ParseDomain(b)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, i := range d.Validate() {
			t.Errorf("%s: %s: %s", f, i.Path, i.Message)
		}
	}
}
