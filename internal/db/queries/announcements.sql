-- name: ListSystemAnnouncements :many
SELECT id, title, body, severity, target, target_tier, target_tenant_ids,
       starts_at, ends_at, created_by, created_at
FROM system_announcements
ORDER BY starts_at DESC
LIMIT $1;

-- name: CreateSystemAnnouncement :one
INSERT INTO system_announcements
    (title, body, severity, target, target_tier, target_tenant_ids,
     starts_at, ends_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, title, body, severity, target, target_tier, target_tenant_ids,
          starts_at, ends_at, created_by, created_at;

-- name: DeleteSystemAnnouncement :execrows
DELETE FROM system_announcements WHERE id = $1;
