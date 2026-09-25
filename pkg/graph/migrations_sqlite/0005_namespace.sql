-- Namespaces: every node lives in one namespace; keys are unique per namespace.
-- The metamodel projection (M:/D: keys) is the "metadata" namespace.
-- SQLite cannot drop the column-level UNIQUE(key): rebuild the node table.
PRAGMA defer_foreign_keys = ON;
CREATE TABLE node_new (
    id        TEXT PRIMARY KEY,
    namespace TEXT NOT NULL DEFAULT 'sdlc',
    key       TEXT NOT NULL,
    type      TEXT NOT NULL,
    latest    INTEGER NOT NULL,
    UNIQUE (namespace, key)
);
INSERT INTO node_new (id, namespace, key, type, latest)
    SELECT id, CASE WHEN key LIKE 'M:%' OR key LIKE 'D:%' THEN 'metadata' ELSE 'sdlc' END, key, type, latest FROM node;
DROP TABLE node;
ALTER TABLE node_new RENAME TO node;
ALTER TABLE change_set ADD COLUMN namespace TEXT NOT NULL DEFAULT 'sdlc';
