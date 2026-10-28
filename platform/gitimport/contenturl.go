package gitimport

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ContentURLOf gives the address of the content of a file at a commit (ADR 0171), the contentUrl of its node, for a
// repository imported from source (a URL or a local directory read in place as dir):
//   - a template, when given, with {commit}, {path} (each element escaped), {blob} and {name} replaced;
//   - a repository of a known forge (github.com, gitlab.com, bitbucket.org, codeberg.org): the raw file at the commit;
//   - a working tree read in place: the file:// URL of the file in it (what is checked out there now);
//
// nil when none applies (a bare clone of another host): the file is named by its blob only.
func ContentURLOf(template, source, dir, name string) func(commit, path, blob string) string {
	if template != "" {
		return func(commit, p, blob string) string {
			return strings.NewReplacer("{commit}", commit, "{path}", escapePath(p), "{blob}", blob, "{name}", name).Replace(template)
		}
	}
	if raw := forgeRaw(source); raw != "" {
		return func(commit, p, _ string) string { return raw + "/" + commit + "/" + escapePath(p) }
	}
	if dir != "" && !strings.Contains(source, "://") && !remoteRE.MatchString(source) {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			base := (&url.URL{Scheme: "file", Path: filepath.ToSlash(dir)}).String()
			return func(_, p, _ string) string { return base + "/" + escapePath(p) }
		}
	}
	return nil
}

// forgeRE reads the host, the owner and the repository of an https or ssh URL of a repository.
var forgeRE = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?|ssh://(?:[^@/]+@)?|[A-Za-z0-9._-]+@)([A-Za-z0-9.-]+)[:/](.+?)(?:\.git)?/?$`)

// forgeRaw is the address of the raw files of a repository of a known forge, "" otherwise: the commit and the path
// follow it.
func forgeRaw(source string) string {
	m := forgeRE.FindStringSubmatch(source)
	if m == nil {
		return ""
	}
	host, repo := strings.ToLower(m[1]), m[2]
	if strings.Count(repo, "/") < 1 {
		return ""
	}
	switch host {
	case "github.com":
		return "https://raw.githubusercontent.com/" + repo
	case "gitlab.com":
		return "https://gitlab.com/" + repo + "/-/raw"
	case "bitbucket.org":
		return "https://bitbucket.org/" + repo + "/raw"
	case "codeberg.org":
		return "https://codeberg.org/" + repo + "/raw/commit"
	}
	return ""
}

// escapePath escapes each element of a slash-separated path.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
