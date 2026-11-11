-- name: CreateTenant :one
INSERT INTO tenants (name, slug)
VALUES ($1, $2)
RETURNING id, name, slug, status, tier, flags, deleted_at, created_at, updated_at;

-- name: GetTenantByID :one
SELECT id, name, slug, status, tier, flags, deleted_at, created_at, updated_at
FROM tenants
WHERE id = $1;

-- name: GetTenantBySlug :one
SELECT id, name, slug, status, tier, flags, deleted_at, created_at, updated_at
FROM tenants
WHERE slug = $1;

-- name: GetTenantStatus :one
SELECT status, deleted_at
FROM tenants
WHERE id = $1;

-- name: ListTenants :many
SELECT id, name, slug, status, tier, flags, deleted_at, created_at, updated_at
FROM tenants
WHERE ($1::bool OR deleted_at IS NULL)
ORDER BY created_at DESC;

-- name: SuspendTenant :exec
UPDATE tenants
SET status = 'suspended', updated_at = now()
WHERE id = $1;

-- name: UnsuspendTenant :exec
UPDATE tenants
SET status = 'active', updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteTenant :exec
UPDATE tenants
SET deleted_at = now(), status = 'suspended', updated_at = now()
WHERE id = $1 AND deleted_at IS NULL;

-- name: RestoreTenant :one
UPDATE tenants
SET deleted_at = NULL, status = 'active', updated_at = now()
WHERE id = $1 AND deleted_at > now() - make_interval(hours => $2::int)
RETURNING id;

-- name: HardDeleteExpiredTenants :many
DELETE FROM tenants
WHERE deleted_at IS NOT NULL
  AND deleted_at < now() - make_interval(hours => $1::int)
RETURNING id;

-- name: PatchTenant :one
-- Super-admin partial-update endpoint for tenant metadata. Each input is
-- nullable; nil leaves the existing value alone. flags is replaced wholesale
-- when provided (merge would require jsonb tracking the FE doesn't yet do).
UPDATE tenants
SET name       = COALESCE(sqlc.narg('name')::text, name),
    tier       = COALESCE(sqlc.narg('tier')::text, tier),
    flags      = COALESCE(sqlc.narg('flags')::jsonb, flags),
    updated_at = now()
WHERE id = $1
RETURNING id, name, slug, status, tier, flags, deleted_at, created_at, updated_at;
