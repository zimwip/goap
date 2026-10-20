-- Smart tags (ADR 0143 §7): shared selections of exact node versions, data of their own (never a state of the graph).
CREATE TABLE smart_tag (
    id          text        PRIMARY KEY,
    name        text        NOT NULL CHECK (name <> ''),
    description text        NOT NULL DEFAULT '',
    owner       text        NOT NULL,
    by          text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL
);
CREATE INDEX smart_tag_owner ON smart_tag (owner);

-- the versions a smart tag selects
CREATE TABLE smart_tag_version (
    tag_id  text    NOT NULL REFERENCES smart_tag (id) ON DELETE CASCADE,
    node_id text    NOT NULL,
    version integer NOT NULL,
    PRIMARY KEY (tag_id, node_id, version)
);
CREATE INDEX smart_tag_version_node ON smart_tag_version (node_id);

-- the users a smart tag is shared with, to read or to edit
CREATE TABLE smart_tag_share (
    tag_id  text NOT NULL REFERENCES smart_tag (id) ON DELETE CASCADE,
    subject text NOT NULL,
    access  text NOT NULL CHECK (access IN ('read', 'edit')),
    PRIMARY KEY (tag_id, subject)
);
CREATE INDEX smart_tag_share_subject ON smart_tag_share (subject);
