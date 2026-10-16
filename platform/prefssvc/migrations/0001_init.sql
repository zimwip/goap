-- User preferences (ADR 0038): one JSON document per user, kept outside the graph.
CREATE TABLE user_preference (
    subject    text        PRIMARY KEY,
    prefs      jsonb       NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);
