-- ADR 0174 addendum: a change may commit several times, each commit writing a version of the impacts it takes. A
-- version names the entry of its change's log that committed it (log_seq: the committed entry of its commit), the
-- position the view of the change is read at to know what made it then (intent, rationale, review).
ALTER TABLE node_version ADD COLUMN log_seq bigint;
