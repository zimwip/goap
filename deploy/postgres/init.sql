-- Dev only: one database, one schema and one role per service.
-- In production each service gets its own database (only GOAP_DB_DSN changes).
DO $$
DECLARE
    svc text;
BEGIN
    FOREACH svc IN ARRAY ARRAY['graph', 'registry', 'engine', 'iam', 'modelgw', 'mcp'] LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = svc) THEN
            EXECUTE format('CREATE ROLE %I LOGIN PASSWORD %L', svc, svc);
        END IF;
        EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I AUTHORIZATION %I', svc, svc);
        EXECUTE format('ALTER ROLE %I SET search_path = %I', svc, svc);
    END LOOP;
END
$$;
