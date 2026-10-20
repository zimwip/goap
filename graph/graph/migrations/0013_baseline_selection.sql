-- ADR 0143: the state of a node at a baseline is a selection of the versions landed, no baseline stores entries. The
-- schema starts from zero without them; a database created before drops what is left (its baseline columns gap, depth
-- and base_id stay, unread).
DROP TABLE IF EXISTS baseline_entry;
DROP INDEX IF EXISTS baseline_stored;
DROP INDEX IF EXISTS baseline_underived;
