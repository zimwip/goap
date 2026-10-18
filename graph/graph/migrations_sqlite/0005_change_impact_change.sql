-- the impacts of a change, in order: every read of a change and of its impacts asks for them by change (the unique
-- index change_impact_live is partial, so it cannot serve WHERE change_id = ... alone: a scan of the whole table)
CREATE INDEX change_impact_change ON change_impact (change_id, seq);
