-- Providers, models and aliases are nodes of the "platform" namespace of the graph (ADR 0021): the gateway
-- keeps only the token usage, keyed by the key of the model node.
CREATE TABLE llm_usage_new (
    model_key text NOT NULL,
    period    text NOT NULL,
    tokens    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (model_key, period)
);
INSERT INTO llm_usage_new (model_key, period, tokens)
    SELECT 'LLM:' || provider || '/' || model, period, tokens FROM llm_usage;
DROP TABLE llm_usage;
ALTER TABLE llm_usage_new RENAME TO llm_usage;
DROP TABLE llm_alias;
DROP TABLE llm_model;
DROP TABLE llm_provider;
