-- name: GetUserPreferences :one
SELECT user_id, tenant_id, language, email_digest, mention_alerts, doc_ready_alerts, updated_at
FROM user_preferences
WHERE user_id = $1;

-- name: UpsertUserPreferences :one
-- Partial-update upsert: NULL inputs preserve the existing value (or the
-- column default on a fresh insert). All four fields are nullable inputs
-- so a PATCH with one field doesn't clobber the others.
INSERT INTO user_preferences (user_id, tenant_id, language, email_digest, mention_alerts, doc_ready_alerts, updated_at)
VALUES (
    $1, $2,
    COALESCE(sqlc.narg('language')::text, 'mn'),
    COALESCE(sqlc.narg('email_digest')::bool, true),
    COALESCE(sqlc.narg('mention_alerts')::bool, true),
    COALESCE(sqlc.narg('doc_ready_alerts')::bool, false),
    now()
)
ON CONFLICT (user_id) DO UPDATE SET
    language         = COALESCE(sqlc.narg('language')::text, user_preferences.language),
    email_digest     = COALESCE(sqlc.narg('email_digest')::bool, user_preferences.email_digest),
    mention_alerts   = COALESCE(sqlc.narg('mention_alerts')::bool, user_preferences.mention_alerts),
    doc_ready_alerts = COALESCE(sqlc.narg('doc_ready_alerts')::bool, user_preferences.doc_ready_alerts),
    updated_at       = now()
RETURNING user_id, tenant_id, language, email_digest, mention_alerts, doc_ready_alerts, updated_at;
