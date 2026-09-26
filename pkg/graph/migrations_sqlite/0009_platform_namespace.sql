-- The "metadata" namespace is renamed "platform" (it also holds the MCPs).
UPDATE node SET namespace = 'platform' WHERE namespace = 'metadata';
UPDATE change_set SET namespace = 'platform' WHERE namespace = 'metadata';
