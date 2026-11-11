BEGIN;

DROP TABLE IF EXISTS system_announcements;
DROP TABLE IF EXISTS tenant_plans;
DROP TABLE IF EXISTS user_preferences;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS refresh_tokens;
DROP TRIGGER IF EXISTS invites_level_ids_tenant ON invites;
DROP FUNCTION IF EXISTS enforce_invite_level_ids_tenant();
DROP TABLE IF EXISTS invites;
DROP TABLE IF EXISTS department_catalog_audit_log;
DROP TABLE IF EXISTS membership_audit_log;
DROP TRIGGER IF EXISTS user_department_memberships_intra_tenant ON user_department_memberships;
DROP FUNCTION IF EXISTS enforce_membership_intra_tenant();
DROP TABLE IF EXISTS user_department_memberships;
DROP TABLE IF EXISTS department_levels;
DROP TABLE IF EXISTS departments;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
DROP FUNCTION IF EXISTS set_updated_at();

COMMIT;
