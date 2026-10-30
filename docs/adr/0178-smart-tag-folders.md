# ADR 0178 — Smart tag folders, and the directory of a project

**Status**: accepted · **Date**: 2026-10 · **Builds on**: ADR 0143 §7 (smart tags), ADR 0039 (projects), ADR 0154
(logical URLs: a node with or without its version), ADR 0157 (every read names its view)

## Context

A link of the graph names the version of its target: a git directory, imported as a `code@Directory` holding its entries
by `contains` links, gets a new version whenever an entry below it does, up to the repository. That is right for the
git directory: it is the organisation of the code, part of its configuration, and its versions are git's trees.

People also arrange nodes to *look* at them: the folders of a project, the documents of a review, a release's
material. Such an arrangement is not a configuration of the nodes it shows. Versioning it with them (a folder getting
a version when a document in it does) or making them suspect is noise, and it should not need a change of the graph
at all. The PLM tools keep the two apart too: a Windchill folder is where a master lives, not a link of the product
structure; a part edited moves no folder.

A smart tag (ADR 0143 §7) is already that kind of data: a named selection of node versions, of any nodes and
namespaces, kept outside the graph, owned and shared, with no change and no journal. It lacks two things to be a folder:
a place in a tree, and an entry that names a node without pinning one of its versions.

## Decision

1. **A smart tag may have a parent tag.** `SmartTag.parent` names another smart tag; tags form trees (a cycle is
   refused, at most `MaxDepth` 16 levels). The root of a tree carries its access: its owner and its shares stand for
   every tag below it (a sub-tag is shared with its root, never alone); a sub-tag records its creator. Creating a
   sub-tag, editing one, moving one within the trees the caller edits needs edit access to the tree; a tag with
   sub-tags is not deleted (empty it first); a root is deleted by its owner, a sub-tag by whoever edits its tree; a
   sub-tag becomes a root of its own only by its creator, and a root keeping shares is not moved under another.
2. **An entry names a node, with or without its version.** A selection holds `(node, version)` pairs as before; version
   0 names the node itself, read at the version the reader's view holds (the head of main, a commit, the working
   change), as a logical URL without `?v=` does (ADR 0154). An exact version stays pinned: a release's material, the
   versions a review is about. Both kinds may sit in one tag. A node-only entry is never suspect and never versioned
   with its node: it follows it.
3. **A project has a directory: the tree of its smart tags.** `SmartTag.project` names a project (its key); a tag of a
   project belongs to it with its whole tree (a sub-tag takes its parent's project). Whoever may work on the project
   (`access.Directory.MayAccessProject`) reads and edits its tags, beside the owner and the shares of each root; the
   project's top-level tags are its directory, shown by the *Directory* pane of the project tab. `ListSmartTags` with
   `project` answers them all, for the web to lay out the tree.
4. **Nothing of the graph changes.** The tags stay in their store (`graph/smarttagsvc`, two columns `parent` and
   `project` in both dialects); no link type, no version, no change. The git directory keeps its exact links and its
   versions up to the root: it is configuration, the project directory is a view.

## Consequences

- The project directory arranges nodes of any namespace, the code of a repository, requirements, documents, without
  writing to them; moving a document between folders is an edit of two tags.
- A node-only entry needs a view to be read (ADR 0157): the web reads it in the context view of the node's namespace.
  A node deleted since stays listed, read as gone.
- Access is computed by walking up to the root (a few reads per tag); the trees are small, bounded by depth.
- Not yet: a folder dragged in the web (moving is a menu), a tag tree as a source of a query, notifications.
