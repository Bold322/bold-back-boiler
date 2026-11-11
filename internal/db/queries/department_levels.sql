-- name: CreateDepartmentLevel :one
INSERT INTO department_levels (department_id, name, rank)
VALUES ($1, $2, $3)
RETURNING id, department_id, name, rank, created_at, updated_at;

-- name: GetDepartmentLevelByID :one
SELECT id, department_id, name, rank, created_at, updated_at
FROM department_levels
WHERE id = $1;

-- name: ListLevelsByDepartment :many
SELECT id, department_id, name, rank, created_at, updated_at
FROM department_levels
WHERE department_id = $1
ORDER BY rank;

-- name: ListLevelsForTenant :many
SELECT
    dl.id, dl.department_id, dl.name, dl.rank,
    d.name AS department_name
FROM department_levels dl
JOIN departments d ON d.id = dl.department_id
WHERE d.tenant_id = $1
  AND d.deprecated_at IS NULL
ORDER BY d.name, dl.rank;

-- name: UpdateDepartmentLevel :one
UPDATE department_levels
SET name = COALESCE(sqlc.narg('name')::text, name),
    rank = COALESCE(sqlc.narg('rank')::int, rank),
    updated_at = now()
WHERE id = $1
RETURNING id, department_id, name, rank, created_at, updated_at;

-- name: DeleteDepartmentLevel :execrows
DELETE FROM department_levels
WHERE id = $1;

-- name: CountDepartmentLevelReferences :one
SELECT
    (SELECT count(*) FROM user_department_memberships WHERE department_level_id = $1)::bigint AS membership_count;
