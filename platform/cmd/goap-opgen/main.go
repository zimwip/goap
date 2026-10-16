// Command goap-opgen generates the catalogue of operations (pkg/operations/operations_gen.go, ADR 0103) from the
// `//goap:operation <code> <kind>` annotations of graph/graph; it fails on a duplicate code or a malformed annotation.
// Run by `go generate ./pkg/operations/` (`make generate`).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zimwip/goap/platform/opgen"
)

func main() {
	src := flag.String("src", "graph/graph", "directory of the annotated sources")
	out := flag.String("out", "contracts/operations/operations_gen.go", "file to write")
	flag.Parse()
	if err := opgen.Generate(*src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "goap-opgen:", err)
		os.Exit(1)
	}
}
