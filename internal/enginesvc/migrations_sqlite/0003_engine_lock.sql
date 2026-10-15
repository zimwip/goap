-- Engine replicas sharing the processes (ADR 0123): when a process was last written (unix milliseconds), to tell the
-- running processes no replica works on, and the leases of the locks the replicas share.
ALTER TABLE process ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX process_running ON process (status, updated_at);
CREATE TABLE engine_lock (
    key         TEXT    PRIMARY KEY,
    holder      TEXT    NOT NULL,
    lease_until INTEGER NOT NULL,
    interrupt   TEXT    NOT NULL DEFAULT ''
);
