CREATE TABLE methodology (
    name         text        NOT NULL,
    version      text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    source       text        NOT NULL,
    goals        jsonb       NOT NULL DEFAULT '[]',
    published_at timestamptz NOT NULL,
    PRIMARY KEY (name, version)
);
