-- MCPs whose tools the llm and script actions of an agent may use (ADR 0019).
ALTER TABLE methodology_agent ADD COLUMN mcps text[] NOT NULL DEFAULT '{}';
