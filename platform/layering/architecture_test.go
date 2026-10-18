package layering

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The architecture overview (docs/architecture-overview.html) names every module of the go.work, every binary of
// platform/cmd and every service of the compose stack, so that a new one cannot be added without drawing it there
// (CLAUDE.md, "Architecture overview"). It checks the names only: the flows are kept up to date by hand.
func TestArchitectureOverview(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "docs", "architecture-overview.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	missing := func(attr, name string) {
		if !strings.Contains(html, attr+`="`+name+`"`) {
			t.Errorf("docs/architecture-overview.html does not show %s %q (%s)", strings.TrimPrefix(attr, "data-"), name, attr)
		}
	}
	for _, m := range modules {
		missing("data-module", m)
	}
	cmds, err := os.ReadDir(filepath.Join("..", "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if c.IsDir() {
			missing("data-binary", c.Name())
		}
	}
	compose, err := os.ReadFile(filepath.Join("..", "..", "deploy", "compose", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// the services run from this repository's image: `entrypoint: ["/goap/<binary>"]` under `  <service>:`
	service := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\n(?:    .*\n)*?    entrypoint: \["/goap/[a-z0-9-]+"\]`)
	found := service.FindAllStringSubmatch(string(compose), -1)
	if len(found) == 0 {
		t.Fatal("no service built from the repository found in deploy/compose/docker-compose.yml")
	}
	for _, f := range found {
		missing("data-service", f[1])
	}
}
