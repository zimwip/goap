-- Graph namespace the changes of a methodology act on (empty: the default, sdlc).
ALTER TABLE methodology ADD COLUMN namespace text NOT NULL DEFAULT '';
