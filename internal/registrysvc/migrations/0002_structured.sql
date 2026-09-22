-- Methodologies are stored as structured, administrable definitions.
-- YAML is an import/export format only (the source column is dropped).
DROP TABLE IF EXISTS methodology;

CREATE TABLE methodology (
    id           uuid PRIMARY KEY,
    name         text        NOT NULL,
    version      text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    status       text        NOT NULL CHECK (status IN ('draft', 'published', 'archived')),
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    published_at timestamptz,
    updated_by   text        NOT NULL DEFAULT '',
    UNIQUE (name, version)
);

CREATE TABLE methodology_node_type (
    methodology_id uuid   NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int    NOT NULL,
    name           text   NOT NULL,
    description    text   NOT NULL DEFAULT '',
    properties     text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (methodology_id, position)
);

CREATE TABLE methodology_link_type (
    methodology_id uuid NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int  NOT NULL,
    name           text NOT NULL,
    from_type      text NOT NULL DEFAULT '',
    to_type        text NOT NULL DEFAULT '',
    PRIMARY KEY (methodology_id, position)
);

CREATE TABLE methodology_condition (
    methodology_id uuid NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int  NOT NULL,
    name           text NOT NULL,
    description    text NOT NULL DEFAULT '',
    expr           text NOT NULL,
    PRIMARY KEY (methodology_id, position)
);

CREATE TABLE methodology_action (
    methodology_id uuid    NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int     NOT NULL,
    name           text    NOT NULL,
    description    text    NOT NULL DEFAULT '',
    kind           text    NOT NULL,
    pre            jsonb   NOT NULL DEFAULT '{}',
    effects        jsonb   NOT NULL DEFAULT '{}',
    cost           double precision NOT NULL DEFAULT 0,
    expects        jsonb,
    permission     text    NOT NULL DEFAULT '',
    model          text    NOT NULL DEFAULT '',
    prompt         text    NOT NULL DEFAULT '',
    tool           text    NOT NULL DEFAULT '',
    builtin        text    NOT NULL DEFAULT '',
    instructions   text    NOT NULL DEFAULT '',
    params         jsonb   NOT NULL DEFAULT '{}',
    PRIMARY KEY (methodology_id, position)
);

CREATE TABLE methodology_goal (
    methodology_id uuid   NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int    NOT NULL,
    name           text   NOT NULL,
    description    text   NOT NULL DEFAULT '',
    examples       text[] NOT NULL DEFAULT '{}',
    pre            jsonb  NOT NULL DEFAULT '{}',
    value          double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (methodology_id, position)
);
