-- Model gateway: providers (API key sealed with AES-GCM), model catalog with
-- global quota and required roles, aliases and token usage per period.
CREATE TABLE llm_provider (
    name        text PRIMARY KEY,
    kind        text NOT NULL,
    protocol    text NOT NULL,
    base_url    text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    api_key_enc text NOT NULL DEFAULT '',
    key_hint    text NOT NULL DEFAULT ''
);
CREATE TABLE llm_model (
    provider     text NOT NULL REFERENCES llm_provider (name) ON DELETE CASCADE,
    model        text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    enabled      boolean NOT NULL DEFAULT true,
    quota_tokens bigint NOT NULL DEFAULT 0,
    quota_period text NOT NULL DEFAULT 'month',
    roles        text NOT NULL DEFAULT '',
    PRIMARY KEY (provider, model)
);
CREATE TABLE llm_alias (
    alias  text PRIMARY KEY,
    target text NOT NULL
);
CREATE TABLE llm_usage (
    provider text NOT NULL,
    model    text NOT NULL,
    period   text NOT NULL,
    tokens   bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, model, period)
);
