# ADR 0069 — Structures and their roots are domain data: the graph core names none of them

**Status**: accepted, implemented · **Date**: 2026-10 · Builds on ADR 0012, 0039, 0054, 0065, 0068.

## Context

ADR 0054 made the two structures of the graph (the organisation and the projects) node types a domain tags, but the
core still carried their names: the root keys (`domain.DefaultOrg`, `domain.DefaultProject`, `OrgOf`, `ProjectOf`),
the property flagging the default project (`PropDefaultProject`), the properties of the two roots (written in
`pkg/graph`'s bootstrap, with English product strings), the fixed list of kinds (`StructureKinds`), and a parallel
hand-written copy of the built-in domain for the untyped graph (`BuiltinStructures`, `BuiltinRequires`,
`BuiltinAdminOnly`, the `Type*` / `Link*` constants).

## Decision

We keep exactly two scope axes, the organisation (who owns a version) and the project (where a node is created), named
by the kinds `domain.StructureOrganisation` / `StructureProject`: they are axis names, not node types. Everything else
comes from the domain.

1. **The tag carries it all.** `structure:` on a node type is `{kind, parent, root, selfParent, default, bootstrap}`:
   `default` names the boolean property flagging the default member (`Graph.DefaultProject` reads it from the
   structure, the smallest flagged key wins, else the root), `bootstrap` gives the initial properties of the root node.
   `domain.Structure` mirrors it; `Graph.Bootstrap` walks the declared structures (organisation, project, then any
   other) and still writes every root in one transaction and one applied change per namespace, with a generic intent.
2. **No built-in copy.** An untyped graph (tests, tools) and a catalogue that lacks a question fall back to
   `typecat.Builtin()`, the catalogue of the built-in domains (`Structure`, `Structures`, `Requires`, `AdminOnly`,
   `IsA`), so the yaml of the organisation domain is the one definition. `domain.BuiltinStructures`,
   `BuiltinStructureSet`, `BuiltinRequires`, `BuiltinAdminOnly` are gone.
3. **Roots are not constants of the core.** `DefaultOrg`, `DefaultProject`, `OrgOf`, `ProjectOf` leave `pkg/domain`.
   Code that needs the root asks the structures (`Graph.Structure(kind).Root`, `domain.Structures`); the constants of
   the built-in organisation (`access.DefaultOrg`, `access.DefaultProject`, the namespace, node types and links
   `access.NamespaceOrganisation`, `NodeTypeOrgUnit`, `LinkPartOf`...) live in `pkg/access`, for the seeds and tests
   of that organisation. The engine no longer defaults `ProcessRef.Org` / `Project` (a stored change always has both);
   the services behind the scope (`mcpsvc.Snapshot.Unit`, the access snapshot) resolve an empty one to the root.
4. **`domain.Structures` is a list** of `StructureSet{Structure, Types}`, one per kind declared, with the typed helpers
   `Organisation()`, `Project()`, `Of(kind)`, `TypesOf(kind)`, `In(kind, type)`. The `GetStructures` RPC answers
   `repeated Structure` (each with its kind, the `default_property` and the `bootstrap` properties).
5. **Validation where it is needed.** The domain schema no longer holds a fixed list of kinds (a kind is required,
   tagged once, the `default` property must be an attribute of the type). The type catalogue checks the parent link of
   every structure and one platform constraint, stated there: the organisation and the projects share a namespace,
   because the services reading them read one head (an Assignment links a unit and a project). The built-in domain
   tags both, so a catalogue always has them.
6. **A guard rail.** `pkg/layering` (`TestCoreNamesNoStructure`) fails when a non-test file of `pkg/graph` or
   `pkg/domain` contains `ORG-DEFAULT`, `PROJ-ROOT`, `OrgUnit`, `ProjectUnit`, `part_of` or `project_part_of`.

## Consequences

- A domain other than the built-in one cannot tag a structure again (one per kind, ADR 0054) but a deployment that
  replaces the organisation domain would change roots, default flag and bootstrap properties without a line of Go.
- Not done here (phase P5b): the shape of `Change.OwnerOrg` / `ProjectID` and `Node.Owner` / `Project`, and the SQL
  columns, still name the two axes.

> The `default` property of a structure is gone (ADR 0091): no change is created without a project, so nothing resolves to a default member.
