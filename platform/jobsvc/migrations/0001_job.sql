-- The jobs (ADR 0149): the long runs a person starts, claimed and reported on by the workers of their kind.
CREATE TABLE job (
    id               text        PRIMARY KEY,
    kind             text        NOT NULL,
    label            text        NOT NULL DEFAULT '',
    target           text        NOT NULL DEFAULT '',
    status           text        NOT NULL,
    cancel_requested boolean     NOT NULL DEFAULT false,
    params           jsonb       NOT NULL DEFAULT '{}',
    by_subject       text        NOT NULL,
    project_id       text        NOT NULL DEFAULT '',
    owner_org        text        NOT NULL DEFAULT '',
    principal        text        NOT NULL DEFAULT '',
    progress         text        NOT NULL DEFAULT '',
    done             bigint      NOT NULL DEFAULT 0,
    total            bigint      NOT NULL DEFAULT 0,
    stats            jsonb       NOT NULL DEFAULT '{}',
    output           jsonb       NOT NULL DEFAULT '{}',
    result_name      text        NOT NULL DEFAULT '',
    result_type      text        NOT NULL DEFAULT '',
    result_size      bigint      NOT NULL DEFAULT 0,
    error            text        NOT NULL DEFAULT '',
    worker           text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL,
    started_at       timestamptz,
    ended_at         timestamptz,
    updated_at       timestamptz NOT NULL,
    lease_until      timestamptz
);
CREATE INDEX job_queue ON job (kind, created_at) WHERE status = 'queued';
CREATE INDEX job_by ON job (by_subject, created_at);
-- The input and the result of a job, in chunks.
CREATE TABLE job_blob (
    job_id text    NOT NULL REFERENCES job (id) ON DELETE CASCADE,
    role   text    NOT NULL,
    seq    integer NOT NULL,
    data   bytea   NOT NULL,
    PRIMARY KEY (job_id, role, seq)
);
-- The outbox of the jobs (ADR 0136): a job created, started, ended, written with it, delivered by the relay.
CREATE TABLE job_outbox (
    seq          bigserial   PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL,
    published_at timestamptz
);
CREATE INDEX job_outbox_pending ON job_outbox (seq) WHERE published_at IS NULL;
