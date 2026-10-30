-- Smart tag folders (ADR 0178): a tag may have a parent tag (its tree), and a tree may belong to a project (the
-- project's directory). An entry of version 0 names the node itself, read at the version the reader's view holds.
ALTER TABLE smart_tag ADD COLUMN parent text NOT NULL DEFAULT '';
ALTER TABLE smart_tag ADD COLUMN project text NOT NULL DEFAULT '';
CREATE INDEX smart_tag_parent ON smart_tag (parent);
CREATE INDEX smart_tag_project ON smart_tag (project);
