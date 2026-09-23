-- Incremental actions: several executions reach the effects (progress is not a failure).
ALTER TABLE methodology_action ADD COLUMN incremental boolean NOT NULL DEFAULT false;
