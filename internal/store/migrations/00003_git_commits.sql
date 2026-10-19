-- +goose Up
CREATE TABLE git_commits (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL,
    sha TEXT NOT NULL,
    parent_shas TEXT[] NOT NULL DEFAULT '{}',
    message TEXT NOT NULL DEFAULT '',
    authored_at TIMESTAMPTZ NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, repository_id, sha),
    FOREIGN KEY (tenant_id, repository_id) REFERENCES repositories(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX git_commits_recent_idx ON git_commits (tenant_id, repository_id, authored_at DESC);

-- +goose Down
DROP TABLE IF EXISTS git_commits;
