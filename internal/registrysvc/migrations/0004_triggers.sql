-- Agent triggers (automatic executions), stored as a JSON array.
ALTER TABLE methodology_agent ADD COLUMN triggers jsonb NOT NULL DEFAULT '[]';
