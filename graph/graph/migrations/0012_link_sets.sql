-- The outgoing links of a node version are a link set (ADR 0141): the rows of link written under a version of the node
-- (from_id, from_version). A version holds its own set (links NULL), the set of an earlier version of the node as is
-- (links = that version: an edit of its attributes writes no link), or its own rows as a delta over the set of another
-- version (links_base, links_depth deltas deep): rows adding links, and rows dropping one (removes, the id of the link of
-- a set it is a delta over).
ALTER TABLE node_version ADD COLUMN links integer;
ALTER TABLE node_version ADD COLUMN links_base integer;
ALTER TABLE node_version ADD COLUMN links_depth integer NOT NULL DEFAULT 0;
ALTER TABLE link ADD COLUMN removes uuid;
CREATE INDEX node_version_links ON node_version (node_id, links);
CREATE INDEX node_version_links_base ON node_version (node_id, links_base);
