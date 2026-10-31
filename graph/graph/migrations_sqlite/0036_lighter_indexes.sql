-- Each version writes fewer index pages, and the pages a graph keeps hot are fewer (the reads of a growing graph past
-- what the memory holds go to the disk):
--   - node_version_branch and node_version_fold: no read names the branch of a version (a branch is read through the
--     commits it holds, branch_commit);
--   - branch_commit_held and branch_commit_seq give way to one index (seq, branch, at): a probe asks a commit of a branch
--     by both and reads the order it was taken at from the index alone, whichever of the two the plan picks; the joins
--     on the order of a commit (the branches holding a version) read it too;
--   - node_version_links and node_version_links_base hold the versions sharing a set or a delta over one only.
DROP INDEX node_version_branch;
DROP INDEX node_version_fold;
DROP INDEX branch_commit_held;
DROP INDEX branch_commit_seq;
CREATE INDEX branch_commit_seq ON branch_commit (seq, branch, at);
DROP INDEX node_version_links;
CREATE INDEX node_version_links ON node_version (links, node_id) WHERE links IS NOT NULL;
DROP INDEX node_version_links_base;
CREATE INDEX node_version_links_base ON node_version (links_base, node_id) WHERE links_base IS NOT NULL;
