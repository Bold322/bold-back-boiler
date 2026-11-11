-- name: CreateDepartment :one
INSERT INTO departments (tenant_id, name, description)
VALUES ($1, $2, $3)
RETURNING id, tenant_id, name, description, deprecated_at, created_at, updated_at;

-- name: GetDepartmentByID :one
SELECT id, tenant_id, name, description, deprecated_at, created_at, updated_at
FROM departments
WHERE id = $1 AND tenant_id = $2;

-- name: ListDepartmentsByTenant :many
SELECT id, tenant_id, name, description, deprecated_at, created_at, updated_at
FROM departments
WHERE tenant_id = $1
  AND ($2::bool OR deprecated_at IS NULL)
ORDER BY name;

-- name: UpdateDepartmentDescription :one
UPDATE departments
SET description = $3, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, name, description, deprecated_at, created_at, updated_at;

-- name: DeprecateDepartment :one
UPDATE departments
SET deprecated_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2 AND deprecated_at IS NULL
RETURNING id, tenant_id, name, description, deprecated_at, created_at, updated_at;

-- name: UndeprecateDepartment :one
UPDATE departments
SET deprecated_at = NULL, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING id, tenant_id, name, description, deprecated_at, created_at, updated_at;

-- name: DeleteDepartment :execrows
DELETE FROM departments
WHERE id = $1 AND tenant_id = $2;

-- name: CountDepartmentReferences :one
-- Rows that still point at any level of this department. Add a column per
-- domain table that references department_levels (e.g. ACL arrays) so
-- DeleteDepartment keeps refusing while the department is in use.
SELECT
    (SELECT count(*)
       FROM user_department_memberships m
       JOIN department_levels dl ON dl.id = m.department_level_id
       WHERE dl.department_id = $1)::bigint                               AS membership_count;
