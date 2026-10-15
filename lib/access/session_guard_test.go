package access_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The web learns the names of the organisation, how its keys are built and what the caller may attempt from the
// session (ADR 0070), in one module. These patterns must not come back anywhere else.
func TestWebMirrorsNothingOfTheSession(t *testing.T) {
	forbidden := map[string]*regexp.Regexp{
		"a hand-built user key":                regexp.MustCompile(`['"` + "`" + `]USR:`),
		"a hand-built assignment key":          regexp.MustCompile(`['"` + "`" + `]ASG:`),
		"a hand-built policy key":              regexp.MustCompile(`['"` + "`" + `]POL:`),
		"a root key":                           regexp.MustCompile(`['"` + "`" + `](ORG-DEFAULT|PROJ-ROOT)['"` + "`" + `]`),
		"a role-name gate":                     regexp.MustCompile(`hasAnyRole\(\s*['"]`),
		"a qualified type of the organisation": regexp.MustCompile(`organisation@\w`),
		"a local namespace constant":           regexp.MustCompile(`const NS(_\w+)? = ['"]`),
	}
	const root = "../../web/src"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		slash := filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".svelte") || strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(slash, "/lib/stores/session.svelte.ts") || strings.Contains(slash, "/lib/help/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for lineNo, line := range strings.Split(string(src), "\n") {
			if c := strings.Index(line, "//"); c >= 0 && strings.TrimSpace(line[:c]) == "" {
				continue // a comment line may name them
			}
			for what, re := range forbidden {
				if re.MatchString(line) {
					t.Errorf("%s:%d: %s: ask the session module (stores/session.svelte.ts) instead: %s", slash, lineNo+1, what, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
