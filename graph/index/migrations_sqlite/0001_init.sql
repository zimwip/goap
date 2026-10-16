-- Document index for the local mode (ADR 0026, 0095: node versions and changes, told apart by kind): full text with FTS5, vectors as float32 BLOBs
-- scanned exactly in Go (no vector extension in SQLite).
CREATE TABLE node_index (
    rowid     INTEGER PRIMARY KEY AUTOINCREMENT,
    kind      TEXT    NOT NULL DEFAULT 'node',
    node_id   TEXT    NOT NULL,
    version   INTEGER NOT NULL,
    namespace TEXT    NOT NULL,
    type      TEXT    NOT NULL,
    key       TEXT    NOT NULL,
    state     TEXT    NOT NULL DEFAULT '',
    branch    TEXT    NOT NULL DEFAULT 'main',
    main      INTEGER NOT NULL DEFAULT 0,
    deleted   INTEGER NOT NULL DEFAULT 0,
    project   TEXT    NOT NULL DEFAULT '',
    owner     TEXT    NOT NULL DEFAULT '',
    status      TEXT  NOT NULL DEFAULT '',
    methodology TEXT  NOT NULL DEFAULT '',
    parent      TEXT  NOT NULL DEFAULT '',
    personal_to TEXT  NOT NULL DEFAULT '',
    title       TEXT  NOT NULL DEFAULT '',
    facets    TEXT    NOT NULL DEFAULT '{}',
    doc       TEXT    NOT NULL DEFAULT '',
    hash      TEXT    NOT NULL DEFAULT '',
    embedding BLOB,
    updated   TEXT    NOT NULL,
    UNIQUE (kind, node_id, version)
);
CREATE INDEX node_index_filter ON node_index (kind, namespace, type, main);
CREATE INDEX node_index_node ON node_index (kind, node_id, main);
CREATE INDEX node_index_project ON node_index (kind, project);

CREATE VIRTUAL TABLE node_fts USING fts5 (doc, tokenize = 'unicode61 remove_diacritics 2');
