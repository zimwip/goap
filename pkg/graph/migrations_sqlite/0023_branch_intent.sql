-- A branch may say why it exists relative to its parent (architecture plan "exploration branches": the same
-- derive/revise/refine vocabulary as an Option's). Empty on a branch opened before this existed.
ALTER TABLE branch ADD COLUMN intent TEXT NOT NULL DEFAULT '';
