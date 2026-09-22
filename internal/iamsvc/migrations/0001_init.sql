-- Casbin ABAC policies (p, <subject rule>, <resource>, <action>, <allow|deny>).
CREATE TABLE casbin_rule (
    id    bigserial PRIMARY KEY,
    ptype text NOT NULL,
    v0    text NOT NULL DEFAULT '',
    v1    text NOT NULL DEFAULT '',
    v2    text NOT NULL DEFAULT '',
    v3    text NOT NULL DEFAULT '',
    v4    text NOT NULL DEFAULT '',
    v5    text NOT NULL DEFAULT '',
    UNIQUE (ptype, v0, v1, v2, v3, v4, v5)
);
