-- Script actions and utilities.
ALTER TABLE methodology_action
    ADD COLUMN language text NOT NULL DEFAULT '',
    ADD COLUMN code     text NOT NULL DEFAULT '',
    ADD COLUMN utility  text NOT NULL DEFAULT '';

-- Agents: a planner and the admissible actions / goals.
CREATE TABLE methodology_agent (
    methodology_id uuid   NOT NULL REFERENCES methodology(id) ON DELETE CASCADE,
    position       int    NOT NULL,
    name           text   NOT NULL,
    description    text   NOT NULL DEFAULT '',
    examples       text[] NOT NULL DEFAULT '{}',
    planner        text   NOT NULL DEFAULT 'goap',
    actions        text[] NOT NULL DEFAULT '{}',
    goals          text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (methodology_id, position)
);
