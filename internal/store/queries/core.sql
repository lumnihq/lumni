-- name: TenantBySlug :one
SELECT id, name, slug, enforcement_mode, created_at, updated_at
FROM tenants
WHERE slug = $1;

-- name: APIKeyByHash :one
SELECT id, tenant_id, name, prefix, scopes, expires_at, revoked_at
FROM api_keys
WHERE secret_hash = $1;

-- name: CurrentResource :one
SELECT tenant_id, id, namespace, type, external_id, current_version,
       version_source, version_sequence, fencing_token
FROM resources
WHERE tenant_id = $1 AND id = $2;
