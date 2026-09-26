-- Flows on change nodes (ADR 0025): the version records the action run that wrote it, a change
-- node the flow that declared it and whether an adopted flow replaced it. A flow may declare
-- a change node for a node the main flow declared (the stale one, superseded once the flow is adopted):
-- the node is unique per flow among the change nodes that are not superseded.
ALTER TABLE node_version ADD COLUMN execution text NOT NULL DEFAULT '';
ALTER TABLE change_node ADD COLUMN flow text NOT NULL DEFAULT '';
ALTER TABLE change_node ADD COLUMN superseded boolean NOT NULL DEFAULT false;
ALTER TABLE change_node DROP CONSTRAINT change_node_change_id_node_id_key;
CREATE UNIQUE INDEX change_node_live ON change_node (change_id, node_id, flow) WHERE NOT superseded;
