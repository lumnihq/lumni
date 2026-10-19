-- +goose Up
CREATE TABLE composio_connections (
 tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
 id TEXT NOT NULL, user_id TEXT NOT NULL UNIQUE,
 repository TEXT NOT NULL, connected_account_id TEXT UNIQUE,
 session_id TEXT, status TEXT NOT NULL DEFAULT 'INITIATED',
 disabled BOOLEAN NOT NULL DEFAULT false,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id)
);
CREATE TABLE composio_calls (
 id BIGSERIAL PRIMARY KEY, tenant_id UUID NOT NULL,
 connection_id TEXT NOT NULL, action_id TEXT,
 kind TEXT NOT NULL, status TEXT NOT NULL, duration_ms BIGINT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,connection_id) REFERENCES composio_connections(tenant_id,id) ON DELETE CASCADE
);
CREATE INDEX composio_calls_connection_idx ON composio_calls(tenant_id,connection_id,created_at);

-- +goose Down
DROP TABLE composio_calls,composio_connections;
