-- Branch identity becomes (namespace, name): a change acts on one namespace (ADR 0015),
-- so "main" is really "main of namespace X" -- two namespaces can each have their own.
-- SQLite cannot change a PRIMARY KEY in place: rebuild the branch table.
PRAGMA defer_foreign_keys = ON;
CREATE TABLE branch_new (
    namespace     TEXT NOT NULL DEFAULT 'default',
    name          TEXT NOT NULL,
    parent        TEXT NOT NULL,
    fork_baseline TEXT REFERENCES baseline(id),
    head_baseline TEXT REFERENCES baseline(id),
    origin        TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    PRIMARY KEY (namespace, name)
);
INSERT INTO branch_new (namespace, name, parent, fork_baseline, head_baseline, origin, status, created_at)
    SELECT 'default', name, parent, fork_baseline, head_baseline, origin, status, created_at FROM branch;
DROP TABLE branch;
ALTER TABLE branch_new RENAME TO branch;
