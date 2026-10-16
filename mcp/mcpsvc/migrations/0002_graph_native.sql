-- MCPs are nodes of the "platform" namespace and adapters nodes of the "organisation"
-- namespace of the graph (ADR 0019): the hub keeps only the live registry of connectors.
DROP TABLE IF EXISTS mcp_binding;
DROP TABLE IF EXISTS mcp_adapter;
DROP TABLE IF EXISTS mcp;
