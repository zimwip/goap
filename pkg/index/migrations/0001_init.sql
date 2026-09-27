-- Node index (ADR 0026): full text with tsvector + GIN, vectors with pgvector, facets as jsonb.
-- The extension lives in public (a service role only sees its own schema): qualified as public.vector.
CREATE EXTENSION IF NOT EXISTS vector SCHEMA public;

CREATE TABLE node_index (
    node_id   text        NOT NULL,
    version   integer     NOT NULL,
    namespace text        NOT NULL,
    type      text        NOT NULL,
    key       text        NOT NULL,
    state     text        NOT NULL DEFAULT '',
    branch    text        NOT NULL DEFAULT 'main',
    main      boolean     NOT NULL DEFAULT false,
    deleted   boolean     NOT NULL DEFAULT false,
    facets    jsonb       NOT NULL DEFAULT '{}',
    doc       text        NOT NULL DEFAULT '',
    hash      text        NOT NULL DEFAULT '',
    embedding public.vector,
    tsv       tsvector GENERATED ALWAYS AS (to_tsvector('simple', doc)) STORED,
    updated   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, version)
);
CREATE INDEX node_index_tsv ON node_index USING gin (tsv);
CREATE INDEX node_index_facets ON node_index USING gin (facets jsonb_path_ops);
CREATE INDEX node_index_filter ON node_index (namespace, type, main);
CREATE INDEX node_index_node ON node_index (node_id, main);
-- The HNSW index needs the dimension of the embedding model: see Postgres.EnsureVectorIndex.
