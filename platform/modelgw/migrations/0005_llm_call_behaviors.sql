-- The global behaviours the gateway added to the instructions of a call (ADR 0093): the names, comma separated ("!name": a
-- behaviour dropped by the cap on the text added to a call), and an estimate of the tokens they added.
ALTER TABLE llm_call ADD COLUMN behaviors       text NOT NULL DEFAULT '';
ALTER TABLE llm_call ADD COLUMN behavior_tokens bigint NOT NULL DEFAULT 0;
