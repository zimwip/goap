-- A change branch holds no baseline (ADR 0140): its state is its fork with the versions landed on it, in the order of
-- the landings. The order is global (a sequence), so a sub-change's fork names the landings of the branches it is
-- forked from by one number (fork_seq).
ALTER TABLE branch ADD COLUMN fork_seq bigint NOT NULL DEFAULT 0;
CREATE SEQUENCE landing_seq;
SELECT setval('landing_seq', greatest(1, (SELECT coalesce(max(seq), 0) FROM baseline), (SELECT coalesce(max(landed), 0) FROM node_version), (SELECT coalesce(max(landed), 0) FROM node_branch)));
