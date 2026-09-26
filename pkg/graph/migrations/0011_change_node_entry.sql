-- Change nodes (ADR 0024): the link change → node with its intent, rationale,
-- pre / post / landed versions and review. The former attachment table (ADR 0014)
-- is kept as change_attachment until the items that fill it are retired.
ALTER TABLE change_node RENAME TO change_attachment;
ALTER INDEX change_node_node RENAME TO change_attachment_node;
ALTER INDEX change_node_pkey RENAME TO change_attachment_pkey;
ALTER INDEX change_node_change_id_node_id_key RENAME TO change_attachment_change_id_node_id_key;
ALTER SEQUENCE change_node_seq_seq RENAME TO change_attachment_seq_seq;

CREATE TABLE change_node (
    id              uuid PRIMARY KEY,
    seq             bigserial,
    change_id       uuid NOT NULL REFERENCES change_set(id),
    node_id         uuid REFERENCES node(id),
    key             text NOT NULL,
    type            text NOT NULL,
    intent          text NOT NULL,
    rationale       text NOT NULL,
    pre_version     int,
    post_version    int,
    landed_version  int,
    review          text NOT NULL DEFAULT 'proposed',
    reviews         jsonb NOT NULL DEFAULT '[]',
    via             uuid REFERENCES change_node(id),
    recheck         boolean NOT NULL DEFAULT false,
    produced_by     text NOT NULL DEFAULT '',
    derived_from    jsonb NOT NULL DEFAULT '[]',
    items           jsonb NOT NULL DEFAULT '[]',
    execution       text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL,
    UNIQUE (change_id, node_id)
);
CREATE INDEX change_node_node ON change_node (node_id);

ALTER TABLE node_version ADD COLUMN change_node uuid;
ALTER TABLE node_version ADD COLUMN comment text NOT NULL DEFAULT '';
