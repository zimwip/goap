-- Organisations (tenants). The default organisation owns every pre-existing change.
CREATE TABLE organization (
    id         text PRIMARY KEY,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO organization (id, name) VALUES ('default', 'Default organisation');
