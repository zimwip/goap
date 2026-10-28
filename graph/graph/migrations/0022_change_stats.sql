-- the counters of what a change committed (domain.ChangeStats): nodes created, modified, deleted, restored, the
-- properties and links they changed, kept at each commit and each merge of a sub-change, for the statistics of the
-- changes
ALTER TABLE change ADD COLUMN stats jsonb NOT NULL DEFAULT '{}';
-- what the last commit of the change did to the node of an impact (domain.ImpactEffect), folded from its committed event
ALTER TABLE change_impact ADD COLUMN effect jsonb NOT NULL DEFAULT '{}';
