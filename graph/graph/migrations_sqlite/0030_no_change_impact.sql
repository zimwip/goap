-- ADR 0174 addendum: a version is the record of its change impact. It names its change and its impact
-- (node_version.change_id, change_impact); what made it (intent, rationale, the review that let it commit, its effect)
-- is read in the view of its change, folded from the log. The table of one row per version goes.
DROP TABLE change_impact;
