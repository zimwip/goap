-- Namespaces: every node lives in one namespace; keys are unique per namespace.
-- The metamodel projection (M:/D: keys) is the "metadata" namespace.
ALTER TABLE node ADD COLUMN namespace text NOT NULL DEFAULT 'sdlc';
UPDATE node SET namespace = 'metadata' WHERE key LIKE 'M:%' OR key LIKE 'D:%';
ALTER TABLE node DROP CONSTRAINT node_key_key;
ALTER TABLE node ADD CONSTRAINT node_namespace_key_key UNIQUE (namespace, key);
ALTER TABLE change_set ADD COLUMN namespace text NOT NULL DEFAULT 'sdlc';
