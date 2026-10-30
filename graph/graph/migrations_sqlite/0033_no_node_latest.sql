-- The latest version of a node is no column kept apart (ADR 0174 addendum: a write updates nothing): it is the greatest
-- version of its rows in node_version, read on its key, and two writers giving a node the same next version meet on
-- that key.
ALTER TABLE node DROP COLUMN latest;
