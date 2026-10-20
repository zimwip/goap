-- the order of a new change impact is the next of the whole table (SQLite: MAX(seq) + 1 at every write of an impact,
-- an update included): without an index on seq alone that is a scan of the table per write (an import writes thousands)
CREATE INDEX change_impact_seq ON change_impact (seq);
