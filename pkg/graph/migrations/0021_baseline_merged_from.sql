-- A merge baseline records its second parent too (ADR 0032): the branch merged in's head at merge time, so its
-- history can show the merge coming back, not just the target branch's own line.
ALTER TABLE baseline ADD COLUMN merged_from uuid REFERENCES baseline(id);
