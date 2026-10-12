-- the guardian chain of a change (ADR 0106): the methodologies that frame it, outermost first, its own last
ALTER TABLE change ADD COLUMN guardians text NOT NULL DEFAULT '[]';
