-- The lists of changes filter by status and by project in the query (Tx.ChangeHeads) rather than reading every change:
-- the open changes of a long-lived graph are a few among many applied ones.
CREATE INDEX change_status ON change (status, created_at);
CREATE INDEX change_project ON change (project_id, created_at);
