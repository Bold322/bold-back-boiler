-- name: ListTenantPlans :many
SELECT id, name, price_mnt, token_limit, seat_limit, storage_limit_gb,
       uploads_per_month, notes, updated_at
FROM tenant_plans
ORDER BY
    CASE id
        WHEN 'starter' THEN 1
        WHEN 'growth' THEN 2
        WHEN 'business' THEN 3
        WHEN 'enterprise' THEN 4
    END;

-- name: UpdateTenantPlan :one
-- Partial update; any nil/NULL field is left untouched. The handler decides
-- whether to forward each field based on what the JSON body included.
UPDATE tenant_plans
SET name              = COALESCE(sqlc.narg('name')::text, name),
    price_mnt         = CASE WHEN sqlc.narg('price_mnt_set')::bool THEN sqlc.narg('price_mnt')::bigint ELSE price_mnt END,
    token_limit       = COALESCE(sqlc.narg('token_limit')::bigint, token_limit),
    seat_limit        = CASE WHEN sqlc.narg('seat_limit_set')::bool THEN sqlc.narg('seat_limit')::int ELSE seat_limit END,
    storage_limit_gb  = CASE WHEN sqlc.narg('storage_limit_gb_set')::bool THEN sqlc.narg('storage_limit_gb')::int ELSE storage_limit_gb END,
    uploads_per_month = CASE WHEN sqlc.narg('uploads_per_month_set')::bool THEN sqlc.narg('uploads_per_month')::int ELSE uploads_per_month END,
    notes             = CASE WHEN sqlc.narg('notes_set')::bool THEN sqlc.narg('notes')::text ELSE notes END,
    updated_at        = now()
WHERE id = $1
RETURNING id, name, price_mnt, token_limit, seat_limit, storage_limit_gb,
          uploads_per_month, notes, updated_at;
