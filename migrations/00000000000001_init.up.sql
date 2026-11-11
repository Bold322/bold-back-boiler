-- Baseline schema for the multi-tenant platform layer. Squashed from the
-- original incremental history into one file so a new product starts from a
-- clean, final-shape schema. Add your own domain tables in NEW migration
-- files (never edit this one once it has been applied anywhere).
--
-- Reads three GUCs injected by migrations.Up as connection startup params:
--   app.default_tenant_name, app.default_tenant_slug  -> the first customer tenant
--   app.platform_tenant_slug                          -> the synthetic tenant that holds superadmins

BEGIN;

-- Extensions --------------------------------------------------------------

CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS citext;    -- case-insensitive emails

-- Shared trigger: keep updated_at in sync on row UPDATE.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

-- Tenants -----------------------------------------------------------------

-- Lifecycle: active -> suspended -> soft-deleted (deleted_at set, restorable
-- within TENANT_DELETE_GRACE_HOURS) -> hard-deleted by tenants.Sweeper
-- (FK cascade removes every tenant-scoped row).
CREATE TABLE tenants (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    -- tier is an advisory reference to tenant_plans.id (kept as text so a
    -- plan rename never needs a coordinated FK change).
    tier        text NOT NULL DEFAULT 'starter',
    -- flags is a flat feature-toggle map ({"beta": true, ...}); the
    -- superadmin UI owns which keys exist.
    flags       jsonb NOT NULL DEFAULT '{}'::jsonb,
    deleted_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_tenants_updated_at
    BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- The platform tenant holds superadmin users. Fixed uuid so tooling can
-- target it without a slug round-trip; undeletable and always active by
-- convention (the API never exposes it in tenant listings).
INSERT INTO tenants (id, name, slug)
VALUES (
    '00000000-0000-0000-0000-000000000099',
    'Platform',
    current_setting('app.platform_tenant_slug')
);

-- The first customer tenant, from DEFAULT_TENANT_NAME / DEFAULT_TENANT_SLUG.
INSERT INTO tenants (name, slug)
VALUES (
    current_setting('app.default_tenant_name'),
    current_setting('app.default_tenant_slug')
);

-- Users -------------------------------------------------------------------

-- user_type gates management routes: admin / worker inside a tenant,
-- superadmin platform-wide (lives in the platform tenant). Soft delete via
-- deleted_at; email uniqueness is GLOBAL among active users so login can
-- resolve a user (and their tenant) from email alone.
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email         citext NOT NULL,
    password_hash text NOT NULL,
    display_name  text NOT NULL CHECK (length(display_name) > 0),
    user_type     text NOT NULL CHECK (user_type IN ('admin', 'worker', 'superadmin')),
    deleted_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_active_unique ON users (email) WHERE deleted_at IS NULL;
CREATE INDEX users_tenant_id_idx   ON users (tenant_id);
CREATE INDEX users_user_type_idx   ON users (user_type);
CREATE INDEX users_deleted_at_idx  ON users (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Departments + levels ----------------------------------------------------

-- Two orthogonal concepts: user_type (above) gates management; a user's
-- (department, level) memberships are the hook for fine-grained access
-- control in your domain tables (e.g. an allowed_level_ids uuid[] column).
CREATE TABLE departments (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          text NOT NULL,
    description   text NOT NULL DEFAULT '',
    deprecated_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE INDEX departments_tenant_idx ON departments (tenant_id) WHERE deprecated_at IS NULL;

CREATE TRIGGER trg_departments_updated_at
    BEFORE UPDATE ON departments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE department_levels (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    department_id uuid NOT NULL REFERENCES departments(id) ON DELETE CASCADE,
    name          text NOT NULL,
    rank          int  NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (department_id, rank),
    UNIQUE (department_id, name)
);
CREATE INDEX department_levels_department_idx ON department_levels (department_id);

CREATE TRIGGER trg_department_levels_updated_at
    BEFORE UPDATE ON department_levels
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE user_department_memberships (
    user_id              uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    department_level_id  uuid NOT NULL REFERENCES department_levels(id) ON DELETE RESTRICT,
    granted_at           timestamptz NOT NULL DEFAULT now(),
    granted_by           uuid REFERENCES users(id),
    PRIMARY KEY (user_id, department_level_id)
);
CREATE INDEX user_department_memberships_level_idx
    ON user_department_memberships (department_level_id);

-- A membership grant must stay inside one tenant. Raises SQLSTATE 23514.
CREATE OR REPLACE FUNCTION enforce_membership_intra_tenant() RETURNS trigger AS $$
DECLARE
    u_tenant uuid;
    d_tenant uuid;
BEGIN
    SELECT tenant_id INTO u_tenant FROM users WHERE id = NEW.user_id;
    SELECT d.tenant_id INTO d_tenant
    FROM department_levels dl
    JOIN departments d ON d.id = dl.department_id
    WHERE dl.id = NEW.department_level_id;
    IF u_tenant IS NULL OR d_tenant IS NULL THEN
        RAISE EXCEPTION 'user_department_memberships: user or level missing' USING ERRCODE = '23503';
    END IF;
    IF u_tenant <> d_tenant THEN
        RAISE EXCEPTION 'user_department_memberships: cross-tenant grant denied' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_department_memberships_intra_tenant
    BEFORE INSERT OR UPDATE ON user_department_memberships
    FOR EACH ROW EXECUTE FUNCTION enforce_membership_intra_tenant();

-- Seed the default tenant's General department with four ordered levels
-- (tenants.Service.CreateTenant does the same for every tenant created later).
WITH default_t AS (
    SELECT id FROM tenants WHERE slug = current_setting('app.default_tenant_slug')
),
new_dept AS (
    INSERT INTO departments (tenant_id, name, description)
    SELECT id, 'General', '' FROM default_t
    RETURNING id
)
INSERT INTO department_levels (department_id, name, rank)
SELECT new_dept.id, v.level_name, v.level_rank
FROM new_dept,
     (VALUES ('Employee', 1), ('Manager', 2), ('Director', 3), ('CEO', 4))
         AS v(level_name, level_rank);

-- Audit logs --------------------------------------------------------------

-- No FK to tenants on purpose: rows survive a tenant hard-delete so the
-- forensic trail outlives the data. Actor/target FKs are RESTRICT, which is
-- why users are soft-deleted rather than removed.
CREATE TABLE membership_audit_log (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid NOT NULL,
    actor_user_id  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    target_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    level_id       uuid NOT NULL,
    level_name     text NOT NULL,
    action         text NOT NULL CHECK (action IN ('grant', 'revoke')),
    at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_membership_audit_log_at     ON membership_audit_log (at DESC);
CREATE INDEX idx_membership_audit_log_tenant ON membership_audit_log (tenant_id, at DESC);
CREATE INDEX idx_membership_audit_log_target ON membership_audit_log (target_user_id, at DESC);

CREATE TABLE department_catalog_audit_log (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL,
    actor_user_id   uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    department_id   uuid NOT NULL,
    department_name text NOT NULL,
    action          text NOT NULL CHECK (action IN ('create','update','deprecate','undeprecate','delete')),
    at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_department_catalog_audit_log_dept ON department_catalog_audit_log (department_id, at DESC);
CREATE INDEX idx_department_catalog_audit_log_at   ON department_catalog_audit_log (at DESC);

-- Invites -----------------------------------------------------------------

-- The only onboarding path (no public signup). invited_by is nullable so the
-- bootstrap admin invite of a brand-new tenant can be issued with no inviter.
-- Only the SHA-256 of the token is stored; the raw token lives in the email.
CREATE TABLE invites (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email        text NOT NULL,
    user_type    text NOT NULL CHECK (user_type IN ('admin', 'worker')),
    level_ids    uuid[] NOT NULL DEFAULT '{}'
                 CHECK (user_type = 'admin' OR array_length(level_ids, 1) >= 1),
    token_hash   bytea NOT NULL UNIQUE,
    expires_at   timestamptz NOT NULL,
    invited_by   uuid REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    accepted_at  timestamptz
);

CREATE UNIQUE INDEX invites_tenant_email_pending_unique
    ON invites (tenant_id, email)
    WHERE accepted_at IS NULL;
CREATE INDEX invites_tenant_pending_idx ON invites (tenant_id, accepted_at, expires_at);

-- level_ids on an invite must all belong to the invite's tenant.
CREATE OR REPLACE FUNCTION enforce_invite_level_ids_tenant() RETURNS trigger AS $$
DECLARE
    bad_count int;
BEGIN
    IF NEW.level_ids IS NULL OR array_length(NEW.level_ids, 1) IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT count(*) INTO bad_count
    FROM unnest(NEW.level_ids) AS lid
    LEFT JOIN department_levels dl ON dl.id = lid
    LEFT JOIN departments d ON d.id = dl.department_id
    WHERE d.tenant_id IS DISTINCT FROM NEW.tenant_id;
    IF bad_count > 0 THEN
        RAISE EXCEPTION 'invite level_ids cross-tenant or unknown' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER invites_level_ids_tenant
    BEFORE INSERT OR UPDATE ON invites
    FOR EACH ROW EXECUTE FUNCTION enforce_invite_level_ids_tenant();

-- Sessions ----------------------------------------------------------------

-- Rotating refresh tokens (SHA-256 stored; raw value in an httpOnly cookie).
-- family_id groups every rotation of one login so reuse detection can revoke
-- the whole lineage; family_started_at anchors the absolute-lifetime cap.
CREATE TABLE refresh_tokens (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id          uuid NOT NULL,
    token_hash         bytea NOT NULL UNIQUE,
    issued_at          timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    family_started_at  timestamptz NOT NULL DEFAULT now(),
    rotated_at         timestamptz,
    revoked_at         timestamptz
);
CREATE INDEX refresh_tokens_tenant_user_idx ON refresh_tokens (tenant_id, user_id);
CREATE INDEX refresh_tokens_family_id_idx   ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_expires_at_idx  ON refresh_tokens (expires_at);

-- Single-use "forgot password" credentials (SHA-256 stored).
CREATE TABLE password_reset_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

-- Per-user preferences ----------------------------------------------------

-- Absent row means "defaults"; the handler never needs to seed on signup.
-- Rename/extend the columns for your product's settings.
CREATE TABLE user_preferences (
    user_id           uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    language          text NOT NULL DEFAULT 'mn',
    email_digest      bool NOT NULL DEFAULT true,
    mention_alerts    bool NOT NULL DEFAULT true,
    doc_ready_alerts  bool NOT NULL DEFAULT false,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_preferences_tenant_id_idx ON user_preferences (tenant_id);

-- Platform ops ------------------------------------------------------------

-- Plan catalog editable by superadmins (pricing/limits without a redeploy).
CREATE TABLE tenant_plans (
    id                  text PRIMARY KEY CHECK (id IN ('starter','growth','business','enterprise')),
    name                text NOT NULL,
    price_mnt           bigint,
    token_limit         bigint NOT NULL,
    seat_limit          int,
    storage_limit_gb    int,
    uploads_per_month   int,
    notes               text,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

INSERT INTO tenant_plans (id, name, price_mnt, token_limit, seat_limit, storage_limit_gb, uploads_per_month, notes)
VALUES
    ('starter',    'Starter',     0,    1000000,   10,   1,   50,   NULL),
    ('growth',     'Growth',      0,    10000000,  100,  10,  500,  NULL),
    ('business',   'Business',    0,    50000000,  500,  100, 5000, NULL),
    ('enterprise', 'Enterprise',  NULL, 200000000, NULL, NULL, NULL, 'Custom contract.');

-- System-wide announcements authored by superadmins. Status (upcoming /
-- active / ended) is derived from starts_at/ends_at, not stored.
CREATE TABLE system_announcements (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title             text NOT NULL,
    body              text NOT NULL,
    severity          text NOT NULL CHECK (severity IN ('info','warning','maintenance')),
    target            text NOT NULL CHECK (target IN ('all','tier','tenants')),
    target_tier       text,
    target_tenant_ids uuid[],
    starts_at         timestamptz NOT NULL,
    ends_at           timestamptz,
    created_by        uuid NOT NULL REFERENCES users(id),
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX system_announcements_starts_at_idx ON system_announcements (starts_at DESC);

COMMIT;
