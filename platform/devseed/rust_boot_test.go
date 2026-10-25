package devseed

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/adapter"
	"github.com/zimwip/goap/lib/llmcfg"
	"github.com/zimwip/goap/lib/mcpbuiltin"
	"github.com/zimwip/goap/platform/modelgw"
)

// rustBoot is where the Rust graph service reads what the platform bootstrap seeds (ADR 0163, graphsvc.Boot and the
// development seeds of this package): the nodes are the code's, so they are written here from it.
const rustBoot = "../../rust/crates/graph-service/src/boot.gen.json"

// seedNode is a node a seed writes: its key, type, properties and, for an adapter, its owner.
type seedNode struct {
	Key   string         `json:"key"`
	Type  string         `json:"type"`
	Props map[string]any `json:"props"`
	Owner string         `json:"owner,omitempty"`
}

// modelCase is a configuration of the model gateway and the nodes SeedModels writes of it, in order.
type modelCase struct {
	Config map[string]any `json:"config"`
	Nodes  []seedNode     `json:"nodes"`
}

// configFile is a configuration as its file (GOAP_MODELS_CONFIG) spells it: the names of its YAML tags.
func configFile(c modelgw.Config) map[string]any {
	provs := map[string]any{}
	for name, p := range c.Providers {
		m := map[string]any{"type": p.Type}
		for k, v := range map[string]string{"baseURL": p.BaseURL, "apiKeySecret": p.APIKeySecret, "apiKeyEnv": p.APIKeyEnv} {
			if v != "" {
				m[k] = v
			}
		}
		provs[name] = m
	}
	return map[string]any{"providers": provs, "aliases": c.Aliases}
}

func modelNodes(t *testing.T, c modelgw.Config) []seedNode {
	provs, models, aliases, err := c.Objects()
	if err != nil {
		t.Fatal(err)
	}
	var out []seedNode
	for _, p := range provs {
		out = append(out, seedNode{Key: llmcfg.ProviderKey(p.Name), Type: llmcfg.NodeTypeProvider, Props: p.Props()})
	}
	for _, m := range models {
		out = append(out, seedNode{Key: m.Key(), Type: llmcfg.NodeTypeModel, Props: m.Props()})
	}
	for _, a := range aliases {
		out = append(out, seedNode{Key: llmcfg.AliasKey(a.Alias), Type: llmcfg.NodeTypeAlias, Props: a.Props()})
	}
	return out
}

func TestRustBootIsCurrent(t *testing.T) {
	var policies []seedNode
	for _, p := range authz.DefaultPolicies {
		policies = append(policies, seedNode{Key: access.PolicyKey(p), Type: access.NodeTypePolicy, Props: access.PolicyProps(p)})
	}
	// the built-in nodes of the platform namespace in the order SeedBuiltins syncs them
	var builtins []seedNode
	var mcps []string
	adapters := map[string]seedNode{}
	defs := mcpbuiltin.AdapterDefs()
	root := access.DefaultOrg
	for i, d := range mcpbuiltin.Defs() {
		mcps = append(mcps, d.Name)
		builtins = append(builtins, seedNode{Key: mcp.MCPKey(d.Name), Type: mcp.NodeTypeMCP, Props: d.Props()},
			seedNode{Key: adapter.DefKey(defs[i].Name), Type: domain.TypeAdapterDef, Props: defs[i].Props()})
		a := mcpbuiltin.Adapter(root, d.Name)
		adapters[d.Name] = seedNode{Key: adapter.Key(a.Unit, a.MCP), Type: domain.TypeAdapter, Props: a.Props(), Owner: a.Unit}
	}
	ws := mcpbuiltin.WorkspaceDef()
	builtins = append(builtins, seedNode{Key: mcp.MCPKey(ws.Name), Type: mcp.NodeTypeMCP, Props: ws.Props()})
	for _, d := range mcpbuiltin.WorkspaceAdapterDefs() {
		builtins = append(builtins, seedNode{Key: adapter.DefKey(d.Name), Type: domain.TypeAdapterDef, Props: d.Props()})
	}
	ca := mcpbuiltin.ClaudeAgentDef()
	builtins = append(builtins, seedNode{Key: mcp.MCPKey(ca.Name), Type: mcp.NodeTypeMCP, Props: ca.Props()},
		seedNode{Key: adapter.DefKey(mcpbuiltin.ClaudeAgent), Type: domain.TypeAdapterDef, Props: mcpbuiltin.ClaudeAgentAdapterDef().Props()})
	for _, r := range access.BuiltinRoles() {
		builtins = append(builtins, seedNode{Key: access.RoleKey(r.Name), Type: access.NodeTypeRole, Props: r.Props()})
	}
	b := llmcfg.TerseBehavior()
	src := graphsvc.PlatformSourceOf()
	d := documentRepository()
	a := localFSAdapterDef()
	kinds := map[string]map[string]string{}
	for _, k := range modelgw.Kinds() {
		kinds[k.ID] = map[string]string{"id": k.ID, "protocol": k.Protocol}
	}
	sample := modelgw.Config{
		Providers: map[string]modelgw.ProviderConfig{
			"local": {Type: "openai", BaseURL: "http://localhost:11434/v1"},
			"main":  {Type: "anthropic", APIKeyEnv: "MAIN_KEY"},
			"z":     {Type: "fake"},
		},
		Aliases: map[string]string{"default": "main/claude", "fast": "local/llama", "embed": "z/hash", "other": "nope/x", "bad": "main", "same": "main/claude"},
	}
	var cases []modelCase
	for _, c := range []modelgw.Config{modelgw.DefaultConfig(true), modelgw.DefaultConfig(false), sample} {
		cases = append(cases, modelCase{Config: configFile(c), Nodes: modelNodes(t, c)})
	}
	raw, err := json.MarshalIndent(map[string]any{
		"comment":            "Code generated from platform/devseed (GOAP_UPDATE_RUST=1 go test ./platform/devseed/ -run TestRustBootIsCurrent). DO NOT EDIT.",
		"floorPolicy":        access.PolicyKey(authz.FloorPolicies[0]),
		"policies":           policies,
		"builtins":           builtins,
		"builtinMCPs":        mcps,
		"adapters":           adapters,
		"behavior":           seedNode{Key: llmcfg.BehaviorKey(b.Name), Type: llmcfg.NodeTypeBehavior, Props: b.Props()},
		"protectedAliases":   llmcfg.ProtectedAliases(),
		"platformSource":     seedNode{Key: domain.SourceKey(src.Name), Type: domain.TypeSource, Props: graphsvc.SourceProps(src)},
		"documentRepository": []seedNode{{Key: mcp.MCPKey(d.Name), Type: mcp.NodeTypeMCP, Props: d.Props()}, {Key: adapter.DefKey(a.Name), Type: domain.TypeAdapterDef, Props: a.Props()}},
		"modelKinds":         kinds,
		"models":             cases,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := string(raw) + "\n"
	if os.Getenv("GOAP_UPDATE_RUST") != "" {
		if err := os.WriteFile(rustBoot, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(rustBoot)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatal(fmt.Sprintf("%s is not what the bootstrap seeds: run GOAP_UPDATE_RUST=1 go test ./platform/devseed/ -run TestRustBootIsCurrent", rustBoot))
	}
}
