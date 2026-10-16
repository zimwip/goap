-- The measured cost of the global behaviours (ADR 0093, "Measured cost"): the input tokens a model reports for an
-- instruction, minus those of the same request with no instruction. A row is valid for one instruction text
-- (instruction_hash) on one model; measured_at is Unix milliseconds; source is measured | estimated (the provider reported
-- no usable difference, so the byte estimate is kept, and not measured again before the TTL).
CREATE TABLE llm_behavior_cost (
    behavior         text    NOT NULL,
    instruction_hash text    NOT NULL,
    provider         text    NOT NULL,
    model            text    NOT NULL,
    tokens           bigint  NOT NULL DEFAULT 0,
    baseline_tokens  bigint  NOT NULL DEFAULT 0,
    measured_at      bigint  NOT NULL DEFAULT 0,
    source           text    NOT NULL DEFAULT 'measured',
    PRIMARY KEY (behavior, instruction_hash, provider, model)
);
-- The input tokens of the minimal calibration request with no instruction, per model.
CREATE TABLE llm_model_baseline (
    provider    text   NOT NULL,
    model       text   NOT NULL,
    tokens      bigint NOT NULL DEFAULT 0,
    measured_at bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, model)
);
-- The tokens a call's behaviours added are a measure or the byte estimate.
ALTER TABLE llm_call ADD COLUMN behavior_tokens_estimated boolean NOT NULL DEFAULT false;
