-- name: ListUsersInTenant :many
-- Returns active users only. include_deleted=true also returns soft-deleted
-- rows so the admin UI can offer restore within the grace window.
SELECT
    u.id, u.tenant_id, u.email, u.display_name, u.user_type, u.created_at, u.deleted_at,
    COALESCE(
        array_agg(dl.id ORDER BY d.name, dl.rank) FILTER (WHERE dl.id IS NOT NULL),
        '{}'
    )::uuid[] AS level_ids,
    COALESCE(
        array_agg(d.name || ' / ' || dl.name ORDER BY d.name, dl.rank)
            FILTER (WHERE dl.id IS NOT NULL),
        '{}'
    )::text[] AS level_labels
FROM users u
LEFT JOIN user_department_memberships m ON m.user_id = u.id
LEFT JOIN department_levels dl ON dl.id = m.department_level_id
LEFT JOIN departments d ON d.id = dl.department_id
WHERE u.tenant_id = $1
  AND (sqlc.arg('include_deleted')::bool OR u.deleted_at IS NULL)
  AND (sqlc.arg('q')::text = ''
       OR u.email ILIKE '%' || sqlc.arg('q')::text || '%'
       OR u.display_name ILIKE '%' || sqlc.arg('q')::text || '%')
GROUP BY u.id
ORDER BY u.created_at;

-- name: InsertMembershipAuditEntry :exec
INSERT INTO membership_audit_log
    (tenant_id, actor_user_id, target_user_id, level_id, level_name, action)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListMembershipAuditEntries :many
SELECT id, tenant_id, actor_user_id, target_user_id, level_id, level_name, action, at
FROM membership_audit_log
WHERE tenant_id = $1
ORDER BY at DESC
LIMIT $2;

-- name: InsertDepartmentCatalogAuditEntry :exec
INSERT INTO department_catalog_audit_log
    (tenant_id, actor_user_id, department_id, department_name, action)
VALUES ($1, $2, $3, $4, $5);

-- name: ListDepartmentCatalogAuditEntries :many
SELECT id, tenant_id, actor_user_id, department_id, department_name, action, at
FROM department_catalog_audit_log
WHERE department_id = $1 AND tenant_id = $2
ORDER BY at DESC
LIMIT $3;

-- name: GetTenantStats :one
-- Aggregated counters for the admin overview dashboard. Extend this query
-- with your own domain counters (orders, projects, ...) as you add tables.
SELECT
    (SELECT COUNT(*) FROM users u WHERE u.tenant_id = sqlc.arg('tenant_id')::uuid AND u.deleted_at IS NULL)::int AS total_members,
    (SELECT COUNT(*) FROM users u WHERE u.tenant_id = sqlc.arg('tenant_id')::uuid AND u.deleted_at IS NULL AND u.user_type = 'admin')::int AS admin_members,
    (SELECT COUNT(*) FROM invites i WHERE i.tenant_id = sqlc.arg('tenant_id')::uuid AND i.accepted_at IS NULL AND i.expires_at > now())::int AS pending_invites,
    (SELECT COUNT(*) FROM departments d WHERE d.tenant_id = sqlc.arg('tenant_id')::uuid AND d.deprecated_at IS NULL)::int AS departments;
