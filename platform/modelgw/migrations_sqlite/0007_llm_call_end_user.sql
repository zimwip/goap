-- The person a call is attributed to (ADR 0114): the subject when a person called, else the person the platform service
-- acted for, carried from one consequence to the next (an event, a trigger, a companion run, a sub-agent). The calls
-- recorded before it are a person's own when their subject is not a platform service.
ALTER TABLE llm_call ADD COLUMN end_user TEXT NOT NULL DEFAULT '';
UPDATE llm_call SET end_user = subject WHERE subject NOT LIKE 'system:%';
CREATE INDEX llm_call_end_user_at ON llm_call (end_user, at_ms);
