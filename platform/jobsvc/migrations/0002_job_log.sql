-- The feedback of a run (ADR 0149): its last progress lines, oldest first.
ALTER TABLE job ADD COLUMN log jsonb NOT NULL DEFAULT '[]';
