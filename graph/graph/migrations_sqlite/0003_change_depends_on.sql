-- the changes a change depends on (ADR 0108): their work reaches the branch it merges into before it merges
ALTER TABLE change ADD COLUMN depends_on text NOT NULL DEFAULT '[]';
