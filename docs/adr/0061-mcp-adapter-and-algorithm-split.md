# ADR 0061 — The MCP, the adapter and the algorithm library are three packages

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0018, 0019, 0028, 0046, 0047, 0060.

## Context

`pkg/mcp` held the definition of an MCP, the adapter (definition, instance, restrictions, code template), the schemas
of the built-in MCPs and the built-in platform roles; `pkg/algo` knew the adapter usage, the `mcp` / `connector`
fields of an algorithm and the secret parameter type. This inverted the design rule that an MCP knows no connector
and no adapter, and that the adapter is the one place organisation, MCP and connector meet; `pkg/access` and
`pkg/llmcfg` imported the MCP package only for constants that already existed in `pkg/domain`.

## Decision

1. `pkg/mcp` keeps the MCP definition: `Def`, `Tool`, scopes, `ToolInfo`, `SplitTool`, `ValidName`, `CheckArgs`,
   `NodeTypeMCP`, and the call context (`CallContext`, `WithCall`, `CallFrom`). It imports nothing of the platform.
2. `pkg/adapter` is the adapter: `Def` (the `platform@AdapterDef` node: MCP, connector, code, params), `Instance` (the
   `organisation@Adapter` node of a unit), `Restriction`, the code `Template` and `Params` derived from a connector,
   and `ParamSecret`. It imports `pkg/mcp` and `pkg/algo`, never the reverse.
3. `pkg/mcpbuiltin` holds the schemas of the built-in MCPs, their adapter definitions and the instance of the default
   organisation (`Defs`, `AdapterDefs`, `Adapter`, `Names`, `Is`).
4. The platform roles (`Role`, `BuiltinRoles`, `RoleKey`, `NodeTypeRole`, `PlatformRoles`) belong to `pkg/access`.
5. The duplicated constants of `pkg/mcp` are gone: `domain.NamespacePlatform`, `domain.NamespaceOrganisation`,
   `domain.TypeOrgUnit`, `domain.LinkPartOf`; the node types of the adapter are `domain.TypeAdapterDef` and
   `domain.TypeAdapter` (so `pkg/access` can gate them without importing the adapter).
6. `pkg/algo` is the generic model of the domains: no `UsageAdapter`, no `MCP` / `Connector` on `Algorithm`, no secret
   parameter type, no `Split`. `Algorithm.Declaration(extraParamTypes...)` is the part of `Issues` that does not
   concern the usage; `adapter.Def.Validate` uses it, declaring `secret` as its own parameter type, and
   `Def.Resolve` returns the connector configuration (non-secret values, checked by `algo`) and the secret
   references (checked by the adapter). A domain declaring an algorithm of type `adapter` gets the "unknown type" issue.
7. The proto `registry.v1.Algorithm` drops `mcp` and `connector` (field numbers 7 and 8 reserved): an adapter is never
   a domain algorithm.
8. `pkg/dsl` keeps `RunAdapter` and `AdapterCtx`: the adapter context shares the script engine of the other
   contexts (`common`, `runScript`) and Go adapter scripts import `dsl.AdapterCtx` from the one symbol table of
   `pkg/dsl`. It no longer checks a usage; `pkg/dsl` imports neither `pkg/mcp` nor `pkg/adapter`.
9. `pkg/layering` tests these rules with `go list -deps`.

## Consequences

- No alias is left behind: every importer (the hub, the built-in connectors, the graph seeds, the access snapshot)
  names the package that owns the symbol.
- `pkg/access` and `pkg/llmcfg` no longer depend on any MCP package.
- The web keeps its `Algorithm` shape for the adapter editor (it is the shape of the `AdapterDef` node's properties).
