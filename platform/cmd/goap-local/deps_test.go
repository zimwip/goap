package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A service waits for those whose URL it is given, but the ones it names lazy; services waiting for each other are
// refused.
func TestDependencies(t *testing.T) {
	services := []service{
		{name: "graph", port: portGraph, env: map[string]string{"GOAP_REGISTRY_URL": url(portRegistry)}, lazy: []string{"registry"}},
		{name: "registry", port: portRegistry, env: map[string]string{"GOAP_GRAPH_URL": url(portGraph)}},
		{name: "modelgw", port: portModelGW, env: map[string]string{"GOAP_GRAPH_URL": url(portGraph)}},
		{name: "mcp", port: portMCP, env: map[string]string{"GOAP_ENGINE_URL": url(portEngine),
			"GOAP_MODEL_PROXY_URL": url(portModelGW) + "/anthropic"}, lazy: []string{"engine"}},
		{name: "engine", port: portEngine, env: map[string]string{"GOAP_MCP_URL": url(portMCP), "GOAP_RUNTIME_URL": url(portEngine)}},
		{name: "importer", env: map[string]string{"GOAP_GRAPH_SOURCES": "content=alm@" + url(portGraph)}},
	}
	if err := dependencies(services); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"graph": nil, "registry": {"graph"}, "modelgw": {"graph"}, "mcp": {"modelgw"},
		"engine": {"mcp"}, "importer": {"graph"}}
	for _, s := range services {
		if !slices.Equal(s.after, want[s.name]) {
			t.Errorf("%s waits for %v, want %v", s.name, s.after, want[s.name])
		}
	}
	services[3].lazy = nil
	if err := dependencies(services); err == nil || !strings.Contains(err.Error(), "waiting for each other") {
		t.Fatalf("cycle: %v", err)
	}
}

// A port is a port: 8080 is not mentioned by a URL of 80801.
func TestMentions(t *testing.T) {
	for v, want := range map[string]bool{
		url(8080):                    true,
		url(8080) + "/anthropic":     true,
		"a=b@" + url(8080) + ",c":    true,
		url(8080) + "1":              false,
		url(8081):                    false,
		url(80801) + " " + url(8080): true,
	} {
		if got := mentions(v, 8080); got != want {
			t.Errorf("mentions(%q) = %v", v, got)
		}
	}
}

// The check of the ports names the one taken.
func TestCheckPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port
	err = checkPorts([]portUse{{taken, "graph"}})
	if err == nil || !strings.Contains(err.Error(), "graph") {
		t.Fatalf("taken: %v", err)
	}
	ln.Close()
	if err := checkPorts([]portUse{{taken, "graph"}}); err != nil {
		t.Fatalf("free: %v", err)
	}
}

// The services start in the order of their dependencies.
func TestStartAllOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	dir := t.TempDir()
	sup := &supervisor{log: slog.New(slog.DiscardHandler), cfg: config{dir: dir}}
	out := filepath.Join(dir, "order")
	step := func(name string, after ...string) service {
		return service{name: name, once: true, path: "sh", dir: dir, args: []string{"-c", "sleep 0.1; echo " + name + " >> " + out}, after: after}
	}
	if err := sup.startAll(context.Background(), []service{step("c", "a", "b"), step("b", "a"), step("a")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != "a\nb\nc\n" {
		t.Fatalf("order %q", got)
	}
}
