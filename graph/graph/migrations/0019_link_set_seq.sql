-- The order of a link set among the sets of its node (ADR 0141 addendum): the set a version writes is the next after the
-- one the version it follows holds; a version sharing a set carries its order. A delta is written over the set of the
-- order with its lowest bit cleared (skip deltas), so its chain is as deep as the bits of its order and holds the
-- changes of a few sets, however many versions share them.
ALTER TABLE node_version ADD COLUMN links_seq integer NOT NULL DEFAULT 0;
