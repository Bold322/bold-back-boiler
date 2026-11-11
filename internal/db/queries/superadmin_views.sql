-- name: ListSuperAudit :many
-- Cross-tenant audit feed: union of membership-change + department-catalog
-- changes. Tenant-name and actor-email are joined in so the FE doesn't have
-- to round-trip per-row. Severity is derived ('warning' for revokes /
-- deletions, 'info' for grants / creates) since neither source table stores
-- one.
SELECT
    ('mem:' || mal.id::text)::text AS id,
    mal.tenant_id,
    t.name           AS tenant_name,
    COALESCE(u.email, mal.actor_user_id::text)::text AS actor_email,
    mal.action,
    ('member:' || mal.target_user_id::text || ' (' || mal.level_name || ')')::text AS resource,
    CASE WHEN mal.action = 'revoke' THEN 'warning' ELSE 'info' END::text AS severity,
    mal.at
FROM membership_audit_log mal
JOIN tenants t ON t.id = mal.tenant_id
LEFT JOIN users u ON u.id = mal.actor_user_id
WHERE (sqlc.narg('tenant_filter')::uuid IS NULL
    OR mal.tenant_id = sqlc.narg('tenant_filter')::uuid)

UNION ALL

SELECT
    ('dep:' || dal.id::text)::text AS id,
    dal.tenant_id,
    t.name           AS tenant_name,
    COALESCE(u.email, dal.actor_user_id::text)::text AS actor_email,
    dal.action,
    ('department:' || dal.department_name)::text AS resource,
    CASE WHEN dal.action IN ('delete','deprecate') THEN 'warning' ELSE 'info' END::text AS severity,
    dal.at
FROM department_catalog_audit_log dal
JOIN tenants t ON t.id = dal.tenant_id
LEFT JOIN users u ON u.id = dal.actor_user_id
WHERE (sqlc.narg('tenant_filter')::uuid IS NULL
    OR dal.tenant_id = sqlc.narg('tenant_filter')::uuid)

ORDER BY at DESC
LIMIT sqlc.arg('row_limit')::int;

-- name: GetPlatformStats :one
SELECT
    (SELECT COUNT(*) FROM tenants WHERE deleted_at IS NULL)::int AS total_tenants,
    (SELECT COUNT(*) FROM tenants WHERE status = 'active' AND deleted_at IS NULL)::int AS active_tenants,
    (SELECT COUNT(*) FROM users WHERE deleted_at IS NULL)::int AS total_members;

-- name: ListPlatformTenantUsers :many
SELECT u.id, u.email, u.display_name, u.user_type, u.created_at
FROM users u
JOIN tenants t ON t.id = u.tenant_id
WHERE t.id = '00000000-0000-0000-0000-000000000099'
ORDER BY u.created_at;
