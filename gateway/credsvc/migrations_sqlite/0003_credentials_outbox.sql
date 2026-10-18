-- The outbox of the credentials service (ADR 0136): the events of a credential or a session, written with it,
-- delivered by the relay. They name the subject and the session, never a hash, a password or a token.
CREATE TABLE credentials_outbox (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    subject      text        NOT NULL,
    payload      text        NOT NULL,
    headers      text        NOT NULL DEFAULT '{}',
    created_at   text        NOT NULL,
    published_at text
);
CREATE INDEX credentials_outbox_pending ON credentials_outbox (seq) WHERE published_at IS NULL;
