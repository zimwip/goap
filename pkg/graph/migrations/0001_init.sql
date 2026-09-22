CREATE TABLE node (
    id     uuid PRIMARY KEY,
    key    text NOT NULL UNIQUE,
    type   text NOT NULL,
    latest int  NOT NULL
);

CREATE TABLE node_version (
    node_id    uuid        NOT NULL REFERENCES node(id),
    version    int         NOT NULL,
    props      jsonb       NOT NULL DEFAULT '{}',
    deleted    boolean     NOT NULL DEFAULT false,
    change_id  uuid,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (node_id, version)
);

CREATE TABLE link (
    id           uuid PRIMARY KEY,
    type         text  NOT NULL,
    from_id      uuid  NOT NULL,
    from_version int   NOT NULL,
    to_id        uuid  NOT NULL,
    to_version   int   NOT NULL,
    props        jsonb NOT NULL DEFAULT '{}',
    change_id    uuid,
    FOREIGN KEY (from_id, from_version) REFERENCES node_version(node_id, version),
    FOREIGN KEY (to_id, to_version) REFERENCES node_version(node_id, version)
);
CREATE INDEX link_from ON link (from_id, from_version);
CREATE INDEX link_to ON link (to_id, to_version);

CREATE TABLE baseline (
    id         uuid PRIMARY KEY,
    name       text        NOT NULL,
    parent_id  uuid REFERENCES baseline(id),
    change_id  uuid,
    created_at timestamptz NOT NULL
);

CREATE TABLE baseline_entry (
    baseline_id uuid NOT NULL REFERENCES baseline(id),
    node_id     uuid NOT NULL,
    version     int  NOT NULL,
    PRIMARY KEY (baseline_id, node_id),
    FOREIGN KEY (node_id, version) REFERENCES node_version(node_id, version)
);

CREATE TABLE change_set (
    id                 uuid PRIMARY KEY,
    title              text        NOT NULL,
    intent             text        NOT NULL DEFAULT '',
    methodology        text        NOT NULL DEFAULT '',
    goal               text        NOT NULL DEFAULT '',
    status             text        NOT NULL,
    baseline_id        uuid        NOT NULL REFERENCES baseline(id),
    result_baseline_id uuid REFERENCES baseline(id),
    data               jsonb       NOT NULL DEFAULT '{}',
    created_at         timestamptz NOT NULL
);

CREATE TABLE change_item (
    id         uuid PRIMARY KEY,
    change_id  uuid        NOT NULL REFERENCES change_set(id),
    seq        bigserial,
    kind       text        NOT NULL,
    -- full item (proposal, decision, data, provenance) as JSON
    payload    jsonb       NOT NULL,
    target_id      uuid,
    target_version int,
    created_at timestamptz NOT NULL
);
CREATE INDEX change_item_change ON change_item (change_id, seq);
CREATE INDEX change_item_target ON change_item (target_id, target_version);
