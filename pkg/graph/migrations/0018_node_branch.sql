-- ADR 0032: a node version is written on one branch (node_version.branch, never changed) and may be part of other
-- branches: a merge that lands a version as is makes it join the target branch instead of copying it.
CREATE TABLE node_branch (
    node_id uuid NOT NULL,
    version int  NOT NULL,
    branch  text NOT NULL,
    PRIMARY KEY (node_id, branch, version),
    FOREIGN KEY (node_id, version) REFERENCES node_version(node_id, version)
);
