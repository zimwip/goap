-- The indexes of the log follow how it is read (ADR 0140): every read of the graph names a change and goes through
-- change_log_change (a type or a flow filters the entries of that range); the engine reads a journal by process and by
-- action run, which only its own entries carry, so those two indexes leave out the entries of the graph.
DROP INDEX change_log_type;
DROP INDEX change_log_flow;
DROP INDEX change_log_process;
DROP INDEX change_log_execution;
CREATE INDEX change_log_process ON change_log (process_id, seq) WHERE process_id <> '';
CREATE INDEX change_log_execution ON change_log (execution) WHERE execution <> '';
