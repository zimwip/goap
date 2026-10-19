-- The branch of a change is ephemeral (ADR 0140): it lives until the change lands, and its states are computed from
-- its fork and what it landed, never stored as checkpoints.
ALTER TABLE branch ADD COLUMN ephemeral boolean NOT NULL DEFAULT false;
UPDATE branch SET ephemeral = true WHERE origin LIKE 'change:%';
