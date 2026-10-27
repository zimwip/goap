-- ADR 0143 §6: a tag names one exact node version. Not unique: a name may label several versions, a version carry
-- several names. A database created before drops the tags of changes (the schema starts from zero).
DROP TABLE IF EXISTS tag;
CREATE TABLE tag (
    id         text PRIMARY KEY,
    name       text NOT NULL CHECK (name <> ''),
    namespace  text NOT NULL,
    node_id    text NOT NULL,
    version    integer NOT NULL,
    by         text NOT NULL DEFAULT '',
    created_at text NOT NULL
);
CREATE INDEX tag_name ON tag (namespace, name);
CREATE INDEX tag_node ON tag (node_id, version);
