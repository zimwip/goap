-- Branch identity becomes (namespace, name): a change acts on one namespace (ADR 0015),
-- so "main" is really "main of namespace X" -- two namespaces can each have their own.
ALTER TABLE branch ADD COLUMN namespace text NOT NULL DEFAULT 'default';
ALTER TABLE branch DROP CONSTRAINT branch_pkey;
ALTER TABLE branch ADD CONSTRAINT branch_pkey PRIMARY KEY (namespace, name);
