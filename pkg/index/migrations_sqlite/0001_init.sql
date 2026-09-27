-- Node index for the local mode (ADR 0026): full text with FTS5, vectors as float32 BLOBs
-- scanned exactly in Go (no vector extension in SQLite).
CREATE TABLE node_index (
    rowid     INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id   TEXT    NOT NULL,
    version   INTEGER NOT NULL,
    namespace TEXT    NOT NULL,
    type      TEXT    NOT NULL,
    key       TEXT    NOT NULL,
    state     TEXT    NOT NULL DEFAULT '',
    branch    TEXT    NOT NULL DEFAULT 'main',
    main      INTEGER NOT NULL DEFAULT 0,
    deleted   INTEGER NOT NULL DEFAULT 0,
    facets    TEXT    NOT NULL DEFAULT '{}',
    doc       TEXT    NOT NULL DEFAULT '',
    hash      TEXT    NOT NULL DEFAULT '',
    embedding BLOB,
    updated   TEXT    NOT NULL,
    UNIQUE (node_id, version)
);
CREATE INDEX node_index_filter ON node_index (namespace, type, main);
CREATE INDEX node_index_node ON node_index (node_id, main);

CREATE VIRTUAL TABLE node_fts USING fts5 (doc, tokenize = 'unicode61 remove_diacritics 2');
