-- name: InsertRefreshToken :exec
INSERT INTO refresh_tokens (tenant_id, user_id, family_id, token_hash, expires_at, family_started_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetRefreshTokenByHash :one
SELECT id, tenant_id, user_id, family_id, token_hash, issued_at, expires_at, family_started_at, rotated_at, revoked_at
FROM refresh_tokens
WHERE token_hash = $1;

-- name: MarkRefreshTokenRotated :execrows
-- Guarded on rotated_at IS NULL so concurrent rotations of the same token
-- yield 0 rows for the loser (the caller treats that as reuse).
UPDATE refresh_tokens
SET rotated_at = now()
WHERE id = $1 AND rotated_at IS NULL AND revoked_at IS NULL;

-- name: RevokeRefreshFamily :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokensForUser :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokensForUserExceptFamily :exec
UPDATE refresh_tokens
SET revoked_at = now()
WHERE user_id = $1 AND family_id <> $2 AND revoked_at IS NULL;

-- name: DeleteDeadRefreshTokens :execrows
-- Sweeper cleanup: drop rows past their sliding expiry, and revoked rows that
-- have aged past the retention window (hours).
DELETE FROM refresh_tokens
WHERE expires_at < now()
   OR (revoked_at IS NOT NULL AND revoked_at < now() - make_interval(hours => $1));
