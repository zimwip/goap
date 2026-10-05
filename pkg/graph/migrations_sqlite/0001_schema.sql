-- Graph schema for the local mode (SQLite), the same model as the PostgreSQL schema: uuids are text, jsonb is JSON
-- text, timestamps are fixed-width RFC 3339 UTC text (sortable), booleans are integers.
-- Graph schema (ADR 0054: the schema starts from zero, earlier migrations were folded into it). Every node version,
-- link and branch membership is written by a change (change_id, NOT NULL, foreign key); every node version is owned
-- by an organisational unit (owner_id) and every node was created in a project (project_id), both nodes of the
-- structures the type catalogue tags. pkg/graph enforces the same rules in Go whatever the storage (guardRepo):
-- these constraints are a second line, not the only one. The foreign keys a bootstrap must satisfy in one
-- transaction (the root unit owns itself, the root project was created in itself, each one references the other)
-- are deferred to the commit.

CREATE TABLE baseline (
    id          text PRIMARY KEY,
    name        text        NOT NULL,
    parent_id   text REFERENCES baseline(id),
    -- the change that produced the baseline (ADR 0056: a baseline is the state a change leaves; the state before
    -- the first change of a namespace is the empty one, it has no row)
    change_id   text        NOT NULL REFERENCES change(id) DEFERRABLE INITIALLY DEFERRED,
    created_at  text        NOT NULL,
    branch      text        NOT NULL DEFAULT 'main',
    namespace   text        NOT NULL DEFAULT 'default',
    depth       integer     NOT NULL DEFAULT 0,
    -- 0: the state is materialised (baseline_entry rows hold it, as a delta from the parent, or whole at a checkpoint);
    -- n > 0: only this header is stored, the state is computed from the baseline n steps back and what the changes in
    -- between did (ADR 0056)
    gap         integer     NOT NULL DEFAULT 0,
    -- commit | merge | fast-forward | snapshot: how the state follows from the change (ADR 0056)
    kind        text        NOT NULL DEFAULT 'snapshot',
    merged_from text REFERENCES baseline(id)
);
CREATE INDEX baseline_namespace ON baseline (namespace, created_at);

CREATE TABLE branch (
    namespace     text        NOT NULL DEFAULT 'default',
    name          text        NOT NULL,
    parent        text        NOT NULL,
    fork_baseline text REFERENCES baseline(id),
    head_baseline text REFERENCES baseline(id),
    origin        text        NOT NULL DEFAULT '',
    status        text        NOT NULL,
    created_at    text        NOT NULL,
    description   text        NOT NULL DEFAULT '',
    intent        text        NOT NULL DEFAULT '',
    PRIMARY KEY (namespace, name)
);

-- owner_org and project_id are the keys of an organisational unit and of a project (ADR 0054): never empty, resolved
-- when the change is created; pkg/graph checks they designate nodes of the tagged structures.
CREATE TABLE change (
    id                 text PRIMARY KEY,
    title              text        NOT NULL,
    intent             text        NOT NULL DEFAULT '',
    methodology        text        NOT NULL DEFAULT '',
    goal               text        NOT NULL DEFAULT '',
    status             text        NOT NULL,
    -- the state the change starts from: the baseline of the change before it; NULL for the empty state (the first
    -- change of a namespace)
    baseline_id        text REFERENCES baseline(id),
    result_baseline_id text REFERENCES baseline(id),
    data               text        NOT NULL DEFAULT '{}',
    created_at         text        NOT NULL,
    branch             text        NOT NULL DEFAULT 'main',
    namespace          text        NOT NULL DEFAULT 'default',
    parent_id          text REFERENCES change(id),
    owner_org          text        NOT NULL CHECK (owner_org <> ''),
    project_id         text        NOT NULL CHECK (project_id <> ''),
    lifecycle          text        NOT NULL DEFAULT '',
    state              text        NOT NULL DEFAULT ''
);
CREATE INDEX change_parent ON change (parent_id);

CREATE TABLE node (
    id         text PRIMARY KEY,
    namespace  text    NOT NULL DEFAULT 'default',
    key        text    NOT NULL,
    type       text    NOT NULL,
    latest     integer NOT NULL,
    -- the project the node was created in
    project_id text    NOT NULL REFERENCES node(id) DEFERRABLE INITIALLY DEFERRED,
    UNIQUE (namespace, key)
);

CREATE TABLE node_version (
    node_id       text        NOT NULL REFERENCES node(id),
    version       integer     NOT NULL,
    props         text        NOT NULL DEFAULT '{}',
    deleted       integer     NOT NULL DEFAULT 0,
    change_id     text        NOT NULL REFERENCES change(id) DEFERRABLE INITIALLY DEFERRED,
    -- the organisational unit responsible for the version
    owner_id      text        NOT NULL REFERENCES node(id) DEFERRABLE INITIALLY DEFERRED,
    created_at    text        NOT NULL,
    branch        text        NOT NULL DEFAULT 'main',
    parents       text        NOT NULL DEFAULT '[]',
    reason        text        NOT NULL DEFAULT '',
    state         text        NOT NULL DEFAULT '',
    change_impact text,
    comment       text        NOT NULL DEFAULT '',
    execution     text        NOT NULL DEFAULT '',
    -- the nodes this one derives from (ADR 0077): [{"id", "version"}], set on the first version of a merge or split successor
    origins       text        NOT NULL DEFAULT '[]',
    PRIMARY KEY (node_id, version)
);
CREATE INDEX node_version_branch ON node_version (node_id, branch, version DESC);
CREATE INDEX node_version_change ON node_version (change_id);

CREATE TABLE link (
    id           text PRIMARY KEY,
    type         text    NOT NULL,
    from_id      text    NOT NULL,
    from_version integer NOT NULL,
    to_id        text    NOT NULL,
    to_version   integer NOT NULL,
    props        text    NOT NULL DEFAULT '{}',
    change_id    text    NOT NULL REFERENCES change(id) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (from_id, from_version) REFERENCES node_version(node_id, version),
    FOREIGN KEY (to_id, to_version) REFERENCES node_version(node_id, version)
);
CREATE INDEX link_from ON link (from_id, from_version);
CREATE INDEX link_to ON link (to_id, to_version);
CREATE INDEX link_change ON link (change_id);

CREATE TABLE baseline_entry (
    baseline_id text    NOT NULL REFERENCES baseline(id),
    node_id     text    NOT NULL,
    version     integer NOT NULL,
    removed     integer NOT NULL DEFAULT 0,
    PRIMARY KEY (baseline_id, node_id),
    FOREIGN KEY (node_id, version) REFERENCES node_version(node_id, version)
);

-- ADR 0032: a node version is written on one branch (node_version.branch, never changed) and may join other branches:
-- a merge that lands a version as is makes it join the target branch instead of copying it. change_id is the change
-- whose merge made it join.
CREATE TABLE node_branch (
    node_id   text    NOT NULL,
    version   integer NOT NULL,
    branch    text    NOT NULL,
    change_id text    NOT NULL REFERENCES change(id) DEFERRABLE INITIALLY DEFERRED,
    PRIMARY KEY (node_id, branch, version),
    FOREIGN KEY (node_id, version) REFERENCES node_version(node_id, version)
);
CREATE INDEX node_branch_change ON node_branch (change_id);

-- A change impact (ADR 0024): what a change does to a node, the projection of its impact events (ADR 0029).
CREATE TABLE change_impact (
    id             text PRIMARY KEY,
    seq            INTEGER NOT NULL,
    change_id      text        NOT NULL REFERENCES change(id),
    node_id        text,
    key            text        NOT NULL,
    type           text        NOT NULL,
    intent         text        NOT NULL,
    rationale      text        NOT NULL,
    pre_version    integer,
    post_version   integer,
    landed_version integer,
    review         text        NOT NULL DEFAULT 'proposed',
    reviews        text        NOT NULL DEFAULT '[]',
    via            text REFERENCES change_impact(id),
    recheck        integer     NOT NULL DEFAULT 0,
    produced_by    text        NOT NULL DEFAULT '',
    derived_from   text        NOT NULL DEFAULT '[]',
    items          text        NOT NULL DEFAULT '[]',
    execution      text        NOT NULL DEFAULT '',
    created_at     text        NOT NULL,
    flow           text        NOT NULL DEFAULT '',
    superseded     integer     NOT NULL DEFAULT 0
);
CREATE INDEX change_impact_node ON change_impact (node_id);
CREATE UNIQUE INDEX change_impact_live ON change_impact (change_id, node_id, flow) WHERE superseded = 0;

-- ADR 0030: one log per change. Facts, journal records and impact events are entries of the same insert-only log, in
-- one order (seq). What an entry is (type), its flow, process, action run and subject are columns to filter on.
CREATE TABLE change_log (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    id         text        NOT NULL UNIQUE,
    change_id  text        NOT NULL REFERENCES change(id),
    -- <stream>.<kind>: fact.artifact, journal.schedule, impact.transitioned...
    type       text        NOT NULL,
    flow       text        NOT NULL DEFAULT '',
    process_id text        NOT NULL DEFAULT '',
    execution  text        NOT NULL DEFAULT '',
    subject    text        NOT NULL DEFAULT '',
    by_whom    text        NOT NULL DEFAULT '',
    at         text        NOT NULL,
    payload    text        NOT NULL
);
CREATE INDEX change_log_change ON change_log (change_id, seq);
CREATE INDEX change_log_type ON change_log (change_id, type, seq);
CREATE INDEX change_log_flow ON change_log (change_id, flow, seq);
CREATE INDEX change_log_process ON change_log (process_id, seq);
CREATE INDEX change_log_execution ON change_log (execution);

-- A tag names the state a change leaves (ADR 0056). Not unique: a name may label several changes. baseline_id is the
-- materialised snapshot of that state, when one is kept.
CREATE TABLE tag (
    id          text PRIMARY KEY,
    name        text NOT NULL CHECK (name <> ''),
    namespace   text NOT NULL,
    change_id   text NOT NULL REFERENCES change(id) DEFERRABLE INITIALLY DEFERRED,
    baseline_id text REFERENCES baseline(id),
    by          text NOT NULL DEFAULT '',
    created_at  text NOT NULL
);
CREATE INDEX tag_name ON tag (namespace, name);
CREATE INDEX tag_change ON tag (change_id);
