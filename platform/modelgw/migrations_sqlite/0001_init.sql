-- Model gateway: providers (API key sealed with AES-GCM), model catalog with
-- global quota and required roles, aliases and token usage per period.
CREATE TABLE llm_provider (
    name        TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    protocol    TEXT NOT NULL,
    base_url    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    api_key_enc TEXT NOT NULL DEFAULT '',
    key_hint    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE llm_model (
    provider     TEXT NOT NULL REFERENCES llm_provider (name) ON DELETE CASCADE,
    model        TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    quota_tokens INTEGER NOT NULL DEFAULT 0,
    quota_period TEXT NOT NULL DEFAULT 'month',
    roles        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider, model)
);
CREATE TABLE llm_alias (
    alias  TEXT PRIMARY KEY,
    target TEXT NOT NULL
);
CREATE TABLE llm_usage (
    provider TEXT NOT NULL,
    model    TEXT NOT NULL,
    period   TEXT NOT NULL,
    tokens   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, model, period)
);
