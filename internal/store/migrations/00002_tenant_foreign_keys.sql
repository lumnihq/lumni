-- +goose Up
ALTER TABLE repositories
    ADD CONSTRAINT repositories_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE agents
    ADD CONSTRAINT agents_repository_tenant_fk
    FOREIGN KEY (tenant_id, repository_id) REFERENCES repositories(tenant_id, id);

ALTER TABLE pull_requests
    DROP CONSTRAINT pull_requests_repository_id_fkey,
    ADD CONSTRAINT pull_requests_repository_tenant_fk
    FOREIGN KEY (tenant_id, repository_id) REFERENCES repositories(tenant_id, id) ON DELETE CASCADE;

ALTER TABLE work
    DROP CONSTRAINT work_repository_id_fkey,
    ADD CONSTRAINT work_repository_tenant_fk
    FOREIGN KEY (tenant_id, repository_id) REFERENCES repositories(tenant_id, id);

ALTER TABLE webhook_endpoints
    ADD CONSTRAINT webhook_endpoints_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE outbox
    ADD CONSTRAINT outbox_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE webhook_deliveries
    DROP CONSTRAINT webhook_deliveries_endpoint_id_fkey,
    DROP CONSTRAINT webhook_deliveries_outbox_id_fkey,
    ADD CONSTRAINT webhook_deliveries_endpoint_tenant_fk
        FOREIGN KEY (tenant_id, endpoint_id) REFERENCES webhook_endpoints(tenant_id, id) ON DELETE CASCADE,
    ADD CONSTRAINT webhook_deliveries_outbox_tenant_fk
        FOREIGN KEY (tenant_id, outbox_id) REFERENCES outbox(tenant_id, id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE webhook_deliveries
    DROP CONSTRAINT webhook_deliveries_endpoint_tenant_fk,
    DROP CONSTRAINT webhook_deliveries_outbox_tenant_fk,
    ADD CONSTRAINT webhook_deliveries_endpoint_id_fkey
        FOREIGN KEY (endpoint_id) REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    ADD CONSTRAINT webhook_deliveries_outbox_id_fkey
        FOREIGN KEY (outbox_id) REFERENCES outbox(id) ON DELETE CASCADE;

ALTER TABLE outbox DROP CONSTRAINT outbox_tenant_id_id_key;
ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_tenant_id_id_key;

ALTER TABLE work
    DROP CONSTRAINT work_repository_tenant_fk,
    ADD CONSTRAINT work_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id);

ALTER TABLE pull_requests
    DROP CONSTRAINT pull_requests_repository_tenant_fk,
    ADD CONSTRAINT pull_requests_repository_id_fkey
        FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE;

ALTER TABLE agents DROP CONSTRAINT agents_repository_tenant_fk;
ALTER TABLE repositories DROP CONSTRAINT repositories_tenant_id_id_key;
