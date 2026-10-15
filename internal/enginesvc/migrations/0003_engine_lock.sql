-- Engine replicas sharing the processes (ADR 0123): when a process was last written (unix milliseconds), to tell the
-- running processes no replica works on, and the leases of the locks the replicas share.
ALTER TABLE process ADD COLUMN updated_at bigint NOT NULL DEFAULT 0;
CREATE INDEX process_running ON process (status, updated_at);
CREATE TABLE engine_lock (
    key         text   PRIMARY KEY,
    holder      text   NOT NULL,
    lease_until bigint NOT NULL,
    interrupt   text   NOT NULL DEFAULT ''
);
