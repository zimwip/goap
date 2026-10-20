# ADR 0151 — Git branches on branches of the graph

**Status**: accepted; implemented · **Date**: 2026-10

## Context

The git importer (ADR 0147) follows one revision, the first-parent history of `HEAD` by default, and writes it on the
main branch of the graph: one change per commit. The other branches of a repository, the work in progress, stayed out
of the graph. The graph already has lasting branches of its own (`CreateBranch`, a change landing on a named branch,
ADR 0032), so a git branch has a natural home: a branch of the graph forked where it leaves the history already there.

One rule stood in the way: a key is one node's, and a creation of a key a node holds outside the reference baseline of
the change was refused while that node lived on a branch not abandoned (`takeKey`). A file a feature branch adds, then
the main branch when the feature is merged, is that case: two creations of the same path on two lines of work.

## Decision

1. **A git branch, a branch of the graph** (`Importer.Branches`, param `branches` of the `git` importer, `code` 1.4.0,
   flag `-branches`): names, `path.Match` patterns or `*` / `all`; the branch `Rev` follows stays on main. Each other
   branch is imported on the branch `<repository>/<git branch>` (`gitimport.GraphBranch`: the branches of a namespace
   are shared by the repositories it holds, main being the line of each one's `Rev`), after main, one change per commit
   of its first-parent line, each change landing on that branch (`NewChange.Branch`); the change's `Data.git.branch`
   names the git branch.
2. **Forked where it leaves what was imported**: the first time, the importer opens the branch of the graph at the
   state the import of the last commit of the branch's first-parent line already imported left, on main or on the
   branch of another git branch (`fork`: the repository node, written by every commit, names the commit of each of its
   versions; the change that wrote the version, or the change of the commit whose sub-change wrote it, gives the
   baseline it landed). A branch sharing no commit with what was imported (an orphan branch) starts from the empty
   state; when the import started at a `Base`, such a branch forked before it is left out.
3. **Resumed like main**: a run reads the head of each branch and imports the commits after the one its repository node
   names; a branch rewritten since (that commit no longer in its history) is left out, said in `Stats.Skipped` and in
   the last line of the job, rather than written over: its branch in the graph keeps the history imported.
4. **A key on two lines of work is one node** (`graph/graph/checkout.go` `takeKey`): a creation takes over a node that
   lives only on other lasting branches (named or main, the node not in the change's reference baseline), writing that
   node's next version as a creation on its own line; the lasting branch the change lands on holding the key is still
   the race of two changes, refused (`lastingLine`), and an open change's work still holds its keys. A merge of two
   lines that both created the node finds no common version: it is a three-way merge with no base (`PlanMerge`,
   `MergeBranch`: the properties and links of both, every differing property a conflict to resolve).

## Consequences

- The work in progress of a repository is in the graph, each branch with its own versions of the files, directories and
  declarations it changed; the nodes it shares with main are the same nodes.
- The importer reads the whole state of a branch once when it switches to it, then follows it commit by commit as on
  main.
- A merge in git is still a commit of main's first-parent line: its tree becomes main's state, the branch it merged
  stays a branch of the graph (nothing closes it; abandon it in the graph when it is no longer wanted).
- Not done: a git branch deleted is not closed in the graph, and the merges of git are not merges of branches of the
  graph.
