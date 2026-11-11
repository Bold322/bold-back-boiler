-- name: CreateUser :one
INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, tenant_id, email, display_name, user_type, created_at, updated_at;

-- name: GetUserByEmail :one
-- Email is globally unique among ACTIVE users (deleted_at IS NULL) via the
-- partial index users_email_active_unique from migration 20260526000000.
-- Login uses this to refuse already-deleted users without leaking existence.
SELECT id, tenant_id, email, password_hash, display_name, user_type, deleted_at, created_at, updated_at
FROM users
WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT id, tenant_id, email, display_name, user_type, deleted_at, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByIDWithPassword :one
SELECT id, tenant_id, email, password_hash, display_name, user_type, deleted_at, created_at, updated_at
FROM users
WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: UpdateUserDisplayName :exec
UPDATE users SET display_name = $2, updated_at = now() WHERE id = $1;

-- name: UpdateUserType :exec
UPDATE users SET user_type = $2, updated_at = now() WHERE id = $1;

-- name: CountAdminsInTenant :one
SELECT count(*)::int AS n
FROM users
WHERE tenant_id = $1 AND user_type = 'admin' AND deleted_at IS NULL;

-- name: SoftDeleteUserInTenant :one
-- Idempotent: returns the affected row on first call, ErrNoRows if the user
-- was already deleted or doesn't exist (callers map this to 404 / 410).
UPDATE users
   SET deleted_at = now(), updated_at = now()
 WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
RETURNING id, tenant_id, email, display_name, user_type, deleted_at;

-- name: SoftDeleteUserCrossTenant :one
-- Superadmin variant: no tenant scope on the WHERE. Caller must verify the
-- target tenant matches their intent via the path :tid.
UPDATE users
   SET deleted_at = now(), updated_at = now()
 WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL
RETURNING id, tenant_id, email, display_name, user_type, deleted_at;

-- name: RestoreUserInTenant :one
-- Restore is bounded by the grace window: deleted_at must be within the
-- caller-supplied interval. Outside the window, the row is considered
-- permanently deleted (the future sweeper will eventually hard-delete).
UPDATE users
   SET deleted_at = NULL, updated_at = now()
 WHERE id = $1
   AND tenant_id = $2
   AND deleted_at IS NOT NULL
   AND deleted_at >= now() - make_interval(hours => $3::int)
RETURNING id, tenant_id, email, display_name, user_type;

-- name: ListUserMemberships :many
SELECT
    dl.id          AS level_id,
    dl.name        AS level_name,
    dl.rank        AS level_rank,
    d.id           AS department_id,
    d.name         AS department_name,
    d.deprecated_at AS department_deprecated_at
FROM user_department_memberships m
JOIN department_levels dl ON dl.id = m.department_level_id
JOIN departments d        ON d.id = dl.department_id
WHERE m.user_id = $1
ORDER BY d.name, dl.rank;

-- name: GetUserLevelIDs :many
SELECT m.department_level_id AS id
FROM user_department_memberships m
WHERE m.user_id = $1
ORDER BY m.department_level_id;
