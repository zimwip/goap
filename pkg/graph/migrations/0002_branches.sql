-- Version branches (ADR 0009): versions are numbered per node across branches,
-- carry their branch, parents and reason; baselines and changes belong to a branch.
ALTER TABLE node_version
    ADD COLUMN branch  text  NOT NULL DEFAULT 'main',
    ADD COLUMN parents int[] NOT NULL DEFAULT '{}',
    ADD COLUMN reason  text  NOT NULL DEFAULT '';
CREATE INDEX node_version_branch ON node_version (node_id, branch, version DESC);

ALTER TABLE baseline ADD COLUMN branch text NOT NULL DEFAULT 'main';
ALTER TABLE change_set ADD COLUMN branch text NOT NULL DEFAULT 'main';

CREATE TABLE branch (
    name          text PRIMARY KEY,
    parent        text        NOT NULL,
    fork_baseline uuid        REFERENCES baseline(id),
    head_baseline uuid        REFERENCES baseline(id),
    origin        text        NOT NULL DEFAULT '',
    status        text        NOT NULL,
    created_at    timestamptz NOT NULL
);
