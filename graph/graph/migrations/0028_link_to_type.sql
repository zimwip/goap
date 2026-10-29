-- ADR 0160: the sources of the links of some types entering a node, read by target and type with the source in the
-- index (an index-only scan for PostgreSQL), in the order of the sources for a page of them (a hub).
CREATE INDEX link_to_type ON link (to_id, type, from_id) WHERE removes IS NULL;
