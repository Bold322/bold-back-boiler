-- name: GrantMembership :exec
INSERT INTO user_department_memberships (user_id, department_level_id, granted_by)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: RevokeMembership :execrows
DELETE FROM user_department_memberships
WHERE user_id = $1 AND department_level_id = $2;

-- name: ListMembershipsForLevel :many
SELECT user_id, department_level_id, granted_at, granted_by
FROM user_department_memberships
WHERE department_level_id = $1
ORDER BY granted_at;

-- name: ListMembershipsForUser :many
SELECT user_id, department_level_id, granted_at, granted_by
FROM user_department_memberships
WHERE user_id = $1
ORDER BY granted_at;
