-- name: ListAllTenantsWithCounts :many
SELECT
    t.id, t.name, t.slug, t.status, t.tier, t.flags, t.deleted_at, t.created_at, t.updated_at,
    mc.member_count::int   AS member_count
FROM tenants t
LEFT JOIN LATERAL (
    SELECT count(*) AS member_count
    FROM users u
    WHERE u.tenant_id = t.id
      AND u.user_type <> 'superadmin'
) mc ON true
WHERE ($1::bool OR t.deleted_at IS NULL)
  AND t.id <> '00000000-0000-0000-0000-000000000099'
ORDER BY t.created_at DESC;

-- name: CountSuperadmins :one
SELECT count(*)::int AS n
FROM users
WHERE user_type = 'superadmin';

-- name: GetSuperadminByEmail :one
SELECT id, tenant_id, email, password_hash, display_name, user_type, created_at, updated_at
FROM users
WHERE email = $1 AND user_type = 'superadmin'
LIMIT 1;

-- name: CreateSuperadmin :one
INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
VALUES ('00000000-0000-0000-0000-000000000099', $1, $2, $3, 'superadmin')
RETURNING id, tenant_id, email, display_name, user_type, created_at, updated_at;
