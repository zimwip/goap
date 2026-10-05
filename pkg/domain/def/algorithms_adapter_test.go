package def

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
)

// A domain knows no adapter: an adapter is an AdapterDef node of the platform namespace (pkg/adapter).
func TestDomainRejectsAdapterAlgorithms(t *testing.T) {
	d := Schema{Algorithms: []algo.Algorithm{{Name: "a", Type: "adapter", Language: algo.JavaScript, Code: "return 1"}}}
	var msgs []string
	d.checkAlgorithms("", func(path, format string, args ...any) { msgs = append(msgs, path+": "+fmt.Sprintf(format, args...)) })
	if len(msgs) == 0 || !strings.Contains(msgs[0], "unknown type") {
		t.Fatalf("issues = %v", msgs)
	}
}
