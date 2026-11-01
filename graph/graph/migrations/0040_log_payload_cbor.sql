-- ADR 0180 §2: the payload of a log entry is bytes, CBOR compressed with the dictionary of its entry type (logcodec.go).
-- An entry written before stays JSON, read as is (its first byte is none of the encoded forms').
ALTER TABLE change_log ALTER COLUMN payload TYPE bytea USING convert_to(payload::text, 'UTF8');
-- The dictionaries, immutable, one entry type each, named by the frames compressed with them.
CREATE TABLE log_dictionary (
    id         bigserial   PRIMARY KEY,
    type       text        NOT NULL,
    dict       bytea       NOT NULL,
    created_at timestamptz NOT NULL
);
