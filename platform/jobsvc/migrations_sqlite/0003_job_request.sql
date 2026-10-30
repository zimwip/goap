-- The request a run asks the person it runs for (ADR 0149 addendum): a form, then its answer.
ALTER TABLE job ADD COLUMN request text NOT NULL DEFAULT '{}';
