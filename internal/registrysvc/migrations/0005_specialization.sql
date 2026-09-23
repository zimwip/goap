-- Action specialization (ADR 0009 §5) and node type subtyping (§6).
ALTER TABLE methodology_action
    ADD COLUMN specializes text NOT NULL DEFAULT '',
    ADD COLUMN when_expr   text NOT NULL DEFAULT '',
    ADD COLUMN priority    int  NOT NULL DEFAULT 0;
ALTER TABLE methodology_node_type ADD COLUMN extends text NOT NULL DEFAULT '';
