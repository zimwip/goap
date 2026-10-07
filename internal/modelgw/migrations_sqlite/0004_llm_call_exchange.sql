-- The exchange of the calls whose prompt is not in a change log (ADR 0089): the request and the answer of every call
-- but an engine call of a change (ADR 0059 keeps those). seq is the seq of the llm_call row; there is no foreign key
-- (the purge deletes both, and a row may be written without its exchange). messages is a JSON array of {role, content}.
CREATE TABLE llm_call_exchange (
    seq       INTEGER PRIMARY KEY,
    system    TEXT    NOT NULL DEFAULT '',
    messages  TEXT    NOT NULL DEFAULT '[]',
    response  TEXT    NOT NULL DEFAULT '',
    truncated INTEGER NOT NULL DEFAULT 0
);
