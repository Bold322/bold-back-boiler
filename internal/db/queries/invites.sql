-- name: CreateInvite :one
INSERT INTO invites (tenant_id, email, user_type, level_ids, token_hash, expires_at, invited_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, email, user_type, level_ids, expires_at, invited_by, created_at, accepted_at;

-- name: GetInviteByTokenHash :one
SELECT id, tenant_id, email, user_type, level_ids, expires_at, invited_by, created_at, accepted_at
FROM invites
WHERE token_hash = $1
  AND accepted_at IS NULL
  AND expires_at > now();

-- name: ListPendingInvitesByTenant :many
SELECT id, tenant_id, email, user_type, level_ids, expires_at, invited_by, created_at, accepted_at
FROM invites
WHERE tenant_id = $1
  AND accepted_at IS NULL
ORDER BY created_at DESC;

-- name: GetInviteByID :one
SELECT id, tenant_id, email, user_type, level_ids, expires_at, invited_by, created_at, accepted_at
FROM invites
WHERE id = $1 AND tenant_id = $2;

-- name: DeleteInviteByID :exec
DELETE FROM invites WHERE id = $1 AND tenant_id = $2;

-- name: MarkInviteAccepted :exec
UPDATE invites SET accepted_at = now() WHERE id = $1;

-- name: ResetInviteToken :exec
UPDATE invites
SET token_hash = $2, expires_at = $3
WHERE id = $1 AND tenant_id = $4 AND accepted_at IS NULL;
