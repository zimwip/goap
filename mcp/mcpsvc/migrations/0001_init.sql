-- MCP hub: generic MCP definitions, adapters (MCP tool -> connector operation),
-- organization bindings and the live registry of connectors. JSON is stored as text.
CREATE TABLE mcp (
    name        text PRIMARY KEY,
    description text NOT NULL DEFAULT '',
    tools       text NOT NULL DEFAULT '[]'
);
CREATE TABLE mcp_adapter (
    mcp       text NOT NULL REFERENCES mcp (name),
    connector text NOT NULL,
    tools     text NOT NULL DEFAULT '[]',
    PRIMARY KEY (mcp, connector)
);
CREATE TABLE mcp_binding (
    org_id    text NOT NULL,
    mcp       text NOT NULL,
    connector text NOT NULL,
    config    text NOT NULL DEFAULT '{}',
    secrets   text NOT NULL DEFAULT '{}',
    PRIMARY KEY (org_id, mcp),
    FOREIGN KEY (mcp, connector) REFERENCES mcp_adapter (mcp, connector)
);
CREATE TABLE connector (
    id           text PRIMARY KEY,
    endpoint     text NOT NULL,
    info         text NOT NULL,
    last_seen_ms bigint NOT NULL
);
