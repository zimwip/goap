-- ADR 0032: a baseline stores what differs from its parent, not a full copy. depth is the distance to the nearest
-- checkpoint of its parent chain: 0 = a checkpoint, whose entries are its whole content (every baseline written before
-- this migration is one); above 0, its entries are the nodes whose version differs from the parent (removed: the node
-- left the baseline, version is the one it had). A baseline is read from its checkpoint, then the deltas down to it.
ALTER TABLE baseline ADD COLUMN depth INTEGER NOT NULL DEFAULT 0;
ALTER TABLE baseline_entry ADD COLUMN removed INTEGER NOT NULL DEFAULT 0;
