-- ADR 0174: a change impact is written in the store only once committed, with the version it produced; before its
-- commit it lives in the log of its change and in the view of the change. The projection of ADR 0029 goes.
DROP TABLE change_impact;
-- one row per version a change wrote: from the version it started from (pre_version) to the version written, the
-- impact behind it and the entry of the change log that recorded the commit (log_seq; 0: none, a bootstrap)
CREATE TABLE change_impact (
    node_id     uuid        NOT NULL,
    version     integer     NOT NULL,
    change_id   uuid        NOT NULL,
    impact_id   uuid,
    key         text        NOT NULL,
    type        text        NOT NULL,
    intent      text        NOT NULL,
    rationale   text        NOT NULL DEFAULT '',
    pre_version integer,
    merged_with integer,
    reason      text        NOT NULL,
    review      jsonb,
    effect      jsonb       NOT NULL DEFAULT '{}',
    log_seq     bigint      NOT NULL DEFAULT 0,
    produced_by text        NOT NULL DEFAULT '',
    execution   text        NOT NULL DEFAULT '',
    via         uuid,
    created_at  timestamptz NOT NULL,
    PRIMARY KEY (node_id, version)
);
CREATE INDEX change_impact_change ON change_impact (change_id, log_seq);
-- the node an impact entry of one event is about: the changes acting on a node, committed or not, read from the log
ALTER TABLE change_log ADD COLUMN node text NOT NULL DEFAULT '';
CREATE INDEX change_log_node ON change_log (node, seq, change_id) WHERE node <> '';
