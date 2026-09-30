-- A branch carries its own metadata now (free-text description of what it is for).
ALTER TABLE branch ADD COLUMN description TEXT NOT NULL DEFAULT '';
