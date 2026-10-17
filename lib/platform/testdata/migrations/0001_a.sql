-- the first migration of the tests
CREATE TABLE a (id text PRIMARY KEY);
CREATE TABLE b (id text PRIMARY KEY, a_id text NOT NULL REFERENCES a(id));
