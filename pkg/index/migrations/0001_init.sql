-- Document index (ADR 0026, 0095): node versions and changes in one table, told apart by kind; full text with tsvector + GIN, vectors with pgvector, facets as jsonb.
-- The extension lives in public (a service role only sees its own schema): qualified as public.vector.
CREATE EXTENSION IF NOT EXISTS vector SCHEMA public;

CREATE TABLE node_index (
    kind      text        NOT NULL DEFAULT 'node',
    node_id   text        NOT NULL,
    version   integer     NOT NULL,
    namespace text        NOT NULL,
    type      text        NOT NULL,
    key       text        NOT NULL,
    state     text        NOT NULL DEFAULT '',
    branch    text        NOT NULL DEFAULT 'main',
    main      boolean     NOT NULL DEFAULT false,
    deleted   boolean     NOT NULL DEFAULT false,
    project   text        NOT NULL DEFAULT '',
    owner     text        NOT NULL DEFAULT '',
    status      text      NOT NULL DEFAULT '',
    methodology text      NOT NULL DEFAULT '',
    parent      text      NOT NULL DEFAULT '',
    personal_to text      NOT NULL DEFAULT '',
    title       text      NOT NULL DEFAULT '',
    facets    jsonb       NOT NULL DEFAULT '{}',
    doc       text        NOT NULL DEFAULT '',
    hash      text        NOT NULL DEFAULT '',
    embedding public.vector,
    tsv       tsvector GENERATED ALWAYS AS (to_tsvector('simple', doc)) STORED,
    updated   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, node_id, version)
);
CREATE INDEX node_index_tsv ON node_index USING gin (tsv);
CREATE INDEX node_index_facets ON node_index USING gin (facets jsonb_path_ops);
CREATE INDEX node_index_filter ON node_index (kind, namespace, type, main);
CREATE INDEX node_index_node ON node_index (kind, node_id, main);
CREATE INDEX node_index_project ON node_index (kind, project);
-- The HNSW index needs the dimension of the embedding model: see Postgres.EnsureVectorIndex.
