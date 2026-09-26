# ADR 0022 — Link types and lifecycles projected as nodes

## Context

Node types are graph-native (ADR 0012) and embed the resolved lifecycle they name (ADR 0014). Link types existed only in the registry
(compile-time lint), and a lifecycle was visible only inside its node types.

## Decision

1. `metamodel.ProjectDomain` (and the own domain of a methodology) also project `LinkType` and `Lifecycle` nodes, with edges
   `NodeType --lifecycle--> Lifecycle` and `LinkType --linkFrom/linkTo--> NodeType`, so that the whole schema of a domain can be navigated on
   the graph. They are derived from the registry, mirrored like the rest of a publication.
2. They are informative. The registry is the authority for node types, link types, lifecycles and algorithms (ADR 0023); the graph enforces only
   what the `NodeType` nodes carry (lifecycle, document, validators). In particular it does not check the node types a link joins, and a change
   may act on any namespace.
3. The resolved lifecycle stays embedded in the `NodeType` node: evaluating it needs one read of the baseline.

## History

A first version of this ADR enforced link types on `add_link` and refused changes on undeclared namespaces (`Namespace` nodes declared by
`graphsvc.SeedNamespaces`). Both checks were removed: they duplicated what the registry validates and made every namespace a piece of
configuration to seed.
