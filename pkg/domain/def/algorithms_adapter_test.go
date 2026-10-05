package def

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/algo"
)

func TestDomainRejectsAdapterAlgorithms(t *testing.T) {
	d := Schema{Algorithms: []algo.Algorithm{{Name: "a", Type: algo.UsageAdapter, Language: algo.JavaScript, Code: "return 1", MCP: "docs", Connector: "fs"}}}
	var msgs []string
	d.checkAlgorithms("", func(path, format string, args ...any) { msgs = append(msgs, path+": "+format) })
	if len(msgs) != 1 || !strings.Contains(msgs[0], "AdapterDef") {
		t.Fatalf("issues = %v", msgs)
	}
}
