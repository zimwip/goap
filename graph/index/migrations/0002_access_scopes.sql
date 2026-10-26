-- ADR 0161 §5: the scope of a node version and the subject who created the node, with its project and its responsible
-- unit (owner), decide who finds it
ALTER TABLE node_index ADD COLUMN visibility text NOT NULL DEFAULT '';
ALTER TABLE node_index ADD COLUMN creator text NOT NULL DEFAULT '';
