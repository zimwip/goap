-- A change branch holds no baseline (ADR 0140): its state is its fork with the versions landed on it, in the order of
-- the landings. The order is global (the greatest landing plus one, the store keeps it in process), so a sub-change's
-- fork names the landings of the branches it is forked from by one number (fork_seq).
ALTER TABLE branch ADD COLUMN fork_seq integer NOT NULL DEFAULT 0;
