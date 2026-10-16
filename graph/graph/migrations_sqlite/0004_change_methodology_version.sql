-- the version of its methodology a change follows: the latest published one when it was created, kept until it is
-- upgraded (ADR 0113)
ALTER TABLE change ADD COLUMN methodology_version text NOT NULL DEFAULT '';
