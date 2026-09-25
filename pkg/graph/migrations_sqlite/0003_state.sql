-- Lifecycle state of a node version (ADR 0014); empty for node types without lifecycle.
ALTER TABLE node_version ADD COLUMN state TEXT NOT NULL DEFAULT '';
