-- MCPs an action uses (ADR 0019): the action is schedulable only where the organization binds them.
ALTER TABLE methodology_action ADD COLUMN mcps jsonb NOT NULL DEFAULT '[]';
