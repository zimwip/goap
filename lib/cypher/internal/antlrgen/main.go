// Command antlrgen generates the Cypher parser of lib/cypher (package parser) from Cypher.g4: it fetches the ANTLR tool
// once into the user cache (checked against its SHA-256), runs it with the Go target, then keeps the Go files only.
// Run from lib/cypher (go generate).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	version = "4.13.1"
	url     = "https://repo1.maven.org/maven2/org/antlr/antlr4/" + version + "/antlr4-" + version + "-complete.jar"
	sum     = "bc13a9c57a8dd7d5196888211e5ede657cb64a3ce968608697e4f668251a8487"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "antlrgen:", err)
		os.Exit(1)
	}
}

func run() error {
	jar, err := tool()
	if err != nil {
		return err
	}
	cmd := exec.Command("java", "-jar", jar, "-Dlanguage=Go", "-visitor", "-no-listener", "-package", "parser",
		"-Xexact-output-dir", "-o", "parser", "Cypher.g4")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("antlr: %w", err)
	}
	entries, err := os.ReadDir("parser")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			if err := os.Remove(filepath.Join("parser", e.Name())); err != nil {
				return err
			}
		}
	}
	if err := dropTricks(filepath.Join("parser", "cypher_parser.go")); err != nil {
		return err
	}
	gofmt := exec.Command("gofmt", "-w", "parser")
	gofmt.Stdout, gofmt.Stderr = os.Stdout, os.Stderr
	return gofmt.Run()
}

// trick is the statement ANTLR ends each rule with so that its label is used: after the return, go vet reports it
// unreachable.
const trick = "\tgoto errorExit // Trick to prevent compiler error if the label is not used\n"

// dropTricks removes the trick, and the label too in the rules that never jump to it (the code after the label is
// reached by falling through).
func dropTricks(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	parts := strings.SplitAfter(string(b), trick)
	var out strings.Builder
	for i, part := range parts {
		if i == len(parts)-1 {
			out.WriteString(part)
			break
		}
		start := strings.LastIndex(part, "\nfunc ") + 1
		head, body := part[:start], strings.TrimSuffix(part[start:], trick)
		if !strings.Contains(body, "goto errorExit") {
			body = strings.Replace(body, "\nerrorExit:\n", "\n", 1)
		}
		out.WriteString(head)
		out.WriteString(body)
	}
	return os.WriteFile(path, []byte(out.String()), 0o644)
}

// tool is the path of the ANTLR jar, fetched into the user cache when missing.
func tool() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "goap", "antlr-"+version+"-complete.jar")
	if b, err := os.ReadFile(path); err == nil && check(b) == nil {
		return path, nil
	}
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := check(b); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o644)
}

func check(b []byte) error {
	h := sha256.Sum256(b)
	if got := hex.EncodeToString(h[:]); got != sum {
		return fmt.Errorf("antlr %s: sha256 %s, want %s", version, got, sum)
	}
	return nil
}
