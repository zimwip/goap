# ADR 0160 — Read queries in a subset of openCypher

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The Graph explorer grows a neighbourhood at a time, from the organisation or from a node a search found. It cannot answer
a question about the shape of the graph: which requirements no test verifies, the needs two hops away from a component,
the links of one type with a property over a threshold. Cypher is the language people already know for that.

The graph is no graph database. Its storage is PostgreSQL, SQLite or memory, and every read of the current graph names a
view (ADR 0157): a branch at a baseline, or a change on a flow with its drafts. Nothing reads a namespace whole. A query
translated to SQL could not express that state: the selection of the versions a baseline holds (ADR 0143), the link sets
held as deltas (ADR 0141), the drafts of a change folded from its log (ADR 0153). A copy of the graph in a graph database
would ignore the views and need keeping in sync.

## Decision

1. **A read-only subset of openCypher, run on the view reader.** A query is `MATCH` clauses (patterns, `WHERE`), then
   `RETURN` (`DISTINCT`, items with `AS`, `*`, `ORDER BY`, `SKIP`, `LIMIT`). The patterns take node labels, inline
   properties, relationship types (`:A|B`), the three directions, variable length (`*`, `*n`, `*n..m`) and path
   variables. Expressions take literals, lists and maps, property access, indexes and slices, `= <> < <= > >=`, `IN`,
   `STARTS WITH`, `ENDS WITH`, `CONTAINS`, `IS [NOT] NULL`, `AND OR XOR NOT`, arithmetic, `CASE`, label tests (`n:Label`),
   the functions `id type labels keys properties nodes relationships length size head last toLower toUpper trim
   toString toInteger toFloat abs coalesce` and the aggregates `count collect sum avg min max`.
   - `OPTIONAL MATCH` (its own `WHERE`; nothing matched keeps the row, its variables null), `EXISTS { pattern [WHERE …] }`
     and pattern predicates (`(r)<-[:verifies]-()`), in `WHERE` and in `RETURN` / `ORDER BY`, negated with `NOT`: a
     variable the row binds is the row's, the others are local to the `EXISTS`.
   - Refused, with the position: writes (`CREATE`, `MERGE`, `SET`, `DELETE`, `REMOVE`), `WITH`, `UNWIND`, `CALL`, `UNION`,
     parameters, comprehensions, quantifiers, `EXISTS` over a query (`EXISTS { MATCH … RETURN … }`) and an `EXISTS`
     inside an `EXISTS`.
2. **The parser is generated from the grammar.** `lib/cypher/Cypher.g4` is the openCypher grammar (release M23, Apache
   2.0); ANTLR 4.13.1, the version of the Go runtime the modules already require through CEL, generates
   `lib/cypher/parser` (`make cypher`, `.\make.ps1 cypher`: `go generate ./cypher/` in `lib`, Java needed, the tool
   fetched once into the user cache and checked against its SHA-256). `lib/cypher` turns the parse tree into a small
   tree (`ast.go`, `parse.go`), refuses what the subset leaves out, evaluates expressions (`eval.go`: null is three-valued,
   every number a float64) and computes `RETURN` (`project.go`: grouping by the items that aggregate nothing). It knows no
   graph.
3. **The match is the graph's, a hop at a time** (`graph/graph/query.go`, `Graph.QueryView`). Each pattern starts from
   an **anchor**: a variable an earlier pattern bound, else a key or an id (`{key: 'R-1'}`, `n.key = 'R-1'` in the WHERE),
   else the nodes of a label, read a page at a time with the text of a WHERE on that node as the page's filter. A pattern
   with no anchor is refused: `MATCH (n) RETURN n` would read the namespace whole. From the anchor the pattern grows left
   and right through the neighbourhoods of the view, each reading the link types its relationship names
   (`Graph.ViewNeighbourhoodOf`: `Tx.LinksFromOfType` / `LinksToOfType`, an unqualified type also read in the view's
   namespace). The conditions of the WHERE are checked as soon as their variables are bound, so a filter prunes the walk.
   A label matches the type, a type extending it, and names its namespace or not (`Requirement` matches
   `alm@Requirement`). A node's `key`, `type`, `state`, `id`, `version`, `namespace`, `owner` and `project` are its own
   fields; any other name is a property. A relationship is used once per clause. The clauses match in order: an
   `OPTIONAL MATCH` checks its own `WHERE` while it matches (a `MATCH`'s `WHERE` may read any variable of the query), and
   binds its variables to null when nothing matched; a later pattern anchored on a null variable matches nothing. An
   `EXISTS` is matched for the row, at its first witness, through the same hops and caps; the ones of `RETURN` and `ORDER
   BY` are matched with the row, their values variables of it. `QueryOptions.Bind` binds variables to nodes before the
   match (RPC `bind`: the node a person queries from, `n`).
4. **Caps, never a whole read.** At most `QueryMaxReads` (2000) reads (neighbourhoods and pages), `QueryMaxAnchors`
   (500) nodes per label anchor, `QueryMaxRows` (2000) rows bound and `QueryMaxDepth` (6) hops for a variable length
   relationship (`*` is read as `*1..6`, said in the notes). A cap stops the match: the answer keeps the rows found,
   flagged `truncated` with a note. A `RETURN` with no aggregate, `DISTINCT` or `ORDER BY` stops once it bound the rows
   its `SKIP` and `LIMIT` keep.
5. **RPC `GraphService.QueryView`** (`view`, `query`): the columns, the rows as JSON lists (a node, a link and a path
   are objects naming them, `{"$node": id}`, `{"$link": id}`, `{"$path": {nodes, links}}`), the nodes and links they
   name, `truncated`, `notes`, `reads`, the baseline read. The router sends it to the graph holding the view's namespace
   (its `view`). Outside a change, a node the caller may not read (`mayReadNode`, ADR 0138) is not in the graph the query
   sees: no pattern, `EXISTS` or `OPTIONAL MATCH` meets it, so `NOT EXISTS` says nothing of it. The check cannot run in
   the read's transaction (an access check reads the graph, and nothing in a graph transaction may wait on such a read):
   the query runs once seeing every node, the nodes it met are asked after it, and when one is hidden it runs again in
   the same baseline without the hidden nodes (`queryMaxRuns` 4; a node met for the first time in a later run is left
   out until asked, and the answer says so). A caller who reads everything it met gets the first run (a query that
   checks then reads to its caps rather than stopping at its `LIMIT`). Through a change the nodes are the change's (`PersonalScope`). `GetNodeNeighbourhood` takes `link_types` too.
6. **The Graph explorer has a query box.** *Query* opens it: the text (Ctrl+Enter runs it), the namespace whose context
   view it reads (the working change on its option for the namespace it acts on, the head of main for the others),
   examples. The nodes and links the rows name replace what the canvas showed, as one neighbourhood whose nodes are not
   expanded yet (each can be expanded from there); the rows are a table under it, a cell naming a node selects it, a double click opens it. A context move runs the query
   again (`web/src/lib/graphQuery.ts`).
   The *Graph explorer* tool of the navigation (`views/nav/GraphTool.svelte`) is its way in: it opens the tab, starts
   from the organisation or from a node found, opens the query box or runs an example (`stores/graphExplorer.svelte.ts`:
   a request the tab takes once).
7. **The Graph pane of a node keeps its compact view and gains a filter and a query** (`components/NodeQuery.svelte`
   above `NodeNeighbourhood.svelte`): the icons and colours of the types and the depth presets (1 to 4 hops) stay; a text
   box filters the nodes shown (key, name, type); a query starts from the node (`n`, bound by `bind`, in the view the
   pane reads, examples of their own) and narrows what is shown to the nodes its rows name (`NodeNeighbourhood`'s
   `keep`: the center always kept, a link kept while both its ends are), its rows a table above the graph (a node of a
   row recenters the pane, a double click opens it).

8. **The query boxes complete from the registry** (`web/src/lib/cypherComplete.ts`, pure; a CodeMirror editor,
   `CodeEditor` `complete` / `onrun`, `completionFrom` and `runKey` of `codemirror.ts`). After `(x:` the node types;
   after `[:` (or `|`) the link types the node on the left holds, its outgoing ones then its entering ones, only the
   entering ones behind `<-`; after `]->(x:` the node types the link reaches first (its sources behind `<-`), read from
   the link's types, else from those the node on the left holds; after `x.` and in `{` the attributes of the variable's
   type (the node's own fields after them, a link's `type` and `id`). A variable's type is its label anywhere in the
   query, else the bound one (`n`, the node of the Graph pane). A type of the namespace read is named bare, another
   escaped with its namespace (`` `code@File` ``); a link whose end names the type ranks before one whose end is open.
   Elsewhere the keywords, the functions and the variables of the query (Ctrl+Space when nothing is typed).

## Consequences

- A question about the shape of the graph is one query, answered in the same view as every other read.
- A query costs what it walks: an anchored pattern with typed hops reads a few neighbourhoods; a broad one stops at a
  cap and says so. The text filter of a label anchor is a superset of what the WHERE keeps, checked once bound.
- A query is not a graph database query: no index on a property (a label anchor pages its type), no write.

## Not yet

- A label anchor reads its type exactly, not its subtypes (the catalogue lists no subtypes); a subtype is matched as a
  later node only.
- `WITH`, `UNWIND`, parameters, `EXISTS` over a query, `COUNT { … }`.
- An anchor from the index (`Search`), the assistant and the `goap-graph` tools calling the query.
