-- ADR 0180 §2: the payload of a log entry is bytes, CBOR compressed with the dictionary of its entry type (logcodec.go),
-- kept as a blob in the column (SQLite stores a blob as is whatever the column's type). An entry written before stays
-- JSON, read as is (its first byte is none of the encoded forms').
-- The dictionaries, immutable, one entry type each, named by the frames compressed with them.
CREATE TABLE log_dictionary (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       text    NOT NULL,
    dict       blob    NOT NULL,
    created_at text    NOT NULL
);
