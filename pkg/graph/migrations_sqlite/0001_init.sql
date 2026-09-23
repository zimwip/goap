-- Graph schema for the local development mode (SQLite). Same model as the
-- PostgreSQL schema: uuids are text, jsonb is JSON text, timestamps are
-- fixed-width RFC 3339 UTC text (sortable).
CREATE TABLE node (
    id     TEXT PRIMARY KEY,
    key    TEXT NOT NULL UNIQUE,
    type   TEXT NOT NULL,
    latest INTEGER NOT NULL
);

CREATE TABLE node_version (
    node_id    TEXT    NOT NULL REFERENCES node(id),
    version    INTEGER NOT NULL,
    props      TEXT    NOT NULL DEFAULT '{}',
    deleted    INTEGER NOT NULL DEFAULT 0,
    change_id  TEXT,
    created_at TEXT    NOT NULL,
    branch     TEXT    NOT NULL DEFAULT 'main',
    parents    TEXT    NOT NULL DEFAULT '[]',
    reason     TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (node_id, version)
);
CREATE INDEX node_version_branch ON node_version (node_id, branch, version DESC);

CREATE TABLE link (
    id           TEXT PRIMARY KEY,
    type         TEXT    NOT NULL,
    from_id      TEXT    NOT NULL,
    from_version INTEGER NOT NULL,
    to_id        TEXT    NOT NULL,
    to_version   INTEGER NOT NULL,
    props        TEXT    NOT NULL DEFAULT '{}',
    change_id    TEXT,
    FOREIGN KEY (from_id, from_version) REFERENCES node_version(node_id, version),
    FOREIGN KEY (to_id, to_version) REFERENCES node_version(node_id, version)
);
CREATE INDEX link_from ON link (from_id, from_version);
CREATE INDEX link_to ON link (to_id, to_version);

CREATE TABLE baseline (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    parent_id  TEXT REFERENCES baseline(id),
    change_id  TEXT,
    created_at TEXT NOT NULL,
    branch     TEXT NOT NULL DEFAULT 'main'
);

CREATE TABLE baseline_entry (
    baseline_id TEXT    NOT NULL REFERENCES baseline(id),
    node_id     TEXT    NOT NULL,
    version     INTEGER NOT NULL,
    PRIMARY KEY (baseline_id, node_id),
    FOREIGN KEY (node_id, version) REFERENCES node_version(node_id, version)
);

CREATE TABLE change_set (
    id                 TEXT PRIMARY KEY,
    title              TEXT NOT NULL,
    intent             TEXT NOT NULL DEFAULT '',
    methodology        TEXT NOT NULL DEFAULT '',
    goal               TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL,
    baseline_id        TEXT NOT NULL REFERENCES baseline(id),
    result_baseline_id TEXT REFERENCES baseline(id),
    data               TEXT NOT NULL DEFAULT '{}',
    created_at         TEXT NOT NULL,
    branch             TEXT NOT NULL DEFAULT 'main'
);

CREATE TABLE change_item (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT NOT NULL UNIQUE,
    change_id      TEXT NOT NULL REFERENCES change_set(id),
    kind           TEXT NOT NULL,
    payload        TEXT NOT NULL,
    target_id      TEXT,
    target_version INTEGER,
    created_at     TEXT NOT NULL
);
CREATE INDEX change_item_change ON change_item (change_id, seq);
CREATE INDEX change_item_target ON change_item (target_id, target_version);

CREATE TABLE branch (
    name          TEXT PRIMARY KEY,
    parent        TEXT NOT NULL,
    fork_baseline TEXT REFERENCES baseline(id),
    head_baseline TEXT REFERENCES baseline(id),
    origin        TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    created_at    TEXT NOT NULL
);
