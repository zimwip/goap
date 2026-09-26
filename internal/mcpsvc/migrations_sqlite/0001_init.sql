-- MCP hub: generic MCP definitions, adapters (MCP tool -> connector operation),
-- organization bindings and the live registry of connectors. JSON is stored as TEXT.
CREATE TABLE mcp (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    tools       TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE mcp_adapter (
    mcp       TEXT NOT NULL REFERENCES mcp (name),
    connector TEXT NOT NULL,
    tools     TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (mcp, connector)
);
CREATE TABLE mcp_binding (
    org_id    TEXT NOT NULL,
    mcp       TEXT NOT NULL,
    connector TEXT NOT NULL,
    config    TEXT NOT NULL DEFAULT '{}',
    secrets   TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (org_id, mcp),
    FOREIGN KEY (mcp, connector) REFERENCES mcp_adapter (mcp, connector)
);
CREATE TABLE connector (
    id           TEXT PRIMARY KEY,
    endpoint     TEXT NOT NULL,
    info         TEXT NOT NULL,
    last_seen_ms INTEGER NOT NULL
);
