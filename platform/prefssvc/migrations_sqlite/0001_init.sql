-- User preferences (ADR 0038): one JSON document per user, kept outside the graph.
CREATE TABLE user_preference (
    subject    TEXT PRIMARY KEY,
    prefs      TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT NOT NULL
);
