-- The branch of a change is ephemeral (ADR 0140): it lives until the change lands, and its states are computed from
-- its fork and what it landed, never stored as checkpoints.
ALTER TABLE branch ADD COLUMN ephemeral integer NOT NULL DEFAULT 0;
UPDATE branch SET ephemeral = 1 WHERE origin LIKE 'change:%';
