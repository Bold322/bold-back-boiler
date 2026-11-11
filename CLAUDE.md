# CLAUDE.md

Guidance for Claude Code (and any agent) working in this repository.

## What this is

`bold-back-boiler` is a Go boilerplate for a multi-tenant SaaS API. It ships
the platform layer only: tenants, users, invite-only onboarding, JWT + rotating
refresh-cookie sessions, admin and superadmin tiers, departments/levels,
email, optional Google sign-in. There is **no product domain** yet; new
features are added as modules under `internal/` following the pattern in
README "Adding a domain module". Keep the platform generic when touching it.

## Build, test, run

```
make run                     # build ./bin/server and run (auto-loads .env)
go build ./... && go vet ./...
go test ./...                # unit tests only; DB tests skip
RUN_DB_TESTS=1 go test ./migrations && RUN_DB_TESTS=1 go test -p=1 ./...
TEST_DATABASE_URL=postgres://...  # overrides the docker-compose test DB
go test -run TestName ./internal/X
sqlc generate                # regenerate internal/db/sqlc from internal/db/queries/*.sql (commit the output)
make db-up / db-down         # local Postgres 17 via docker compose (port 15432)
make seed NAME=... SLUG=... EMAIL=...   # tenant + first-admin invite
make superadmin EMAIL=... NAME=... PASS=...
```

Run `./migrations` tests first when running integration tests: they reset the
schema; the other packages assume a migrated DB. `-p=1` keeps per-package
truncate cleanups from racing. CI mirrors this in `.github/workflows/ci.yml`.

No config file: everything is env (`internal/config/config.go` validates and
fails fast). Required: `POSTGRES_DB/USER/PASSWORD`, `JWT_SECRET` (≥32 chars),
`DEFAULT_TENANT_NAME/SLUG`. See `.env.example` for the full annotated list.

## Architecture

```
cmd/server/main.go
  -> config.Load                  env, validates
  -> db.NewPool                   pgxpool
  -> migrations.Up                embedded SQL, advisory-locked, injects app.default_tenant_* / app.platform_tenant_slug GUCs
  -> superadmin bootstrap         optional, idempotent (SUPERADMIN_* env)
  -> email.Sender                 SES or NoopSender (logs)
  -> auth.Service / tenants / invites / admin / superadmin services
  -> server.New(cfg, pool, server.Deps{...})   Echo router (route map lives here)
  -> tenants.Sweeper + auth.RefreshSweeper goroutines (own advisory locks)
```

`internal/server/server.go` is the canonical route map. Groups:

- **public**: `/health`, `/auth/login` (`{email,password}`; email is globally
  unique among active users so the tenant is read off the matched row),
  `/auth/accept-invite`, `/auth/forgot-password`, `/auth/reset-password`,
  `/auth/refresh`, `/auth/logout` (all behind per-IP `RateLimit`);
  `/auth/google/*` only when `GOOGLE_OAUTH_*` is configured.
- **authed** (`RequireJWT + RequireActiveTenant`): `/me`, `/preferences`,
  `GET /departments`. Add domain routes here.
- **admin** (`+ RequireAdmin`, from `claims.IsAdmin()`): `/admin/users`,
  `/admin/departments`, `/admin/invites`, `/admin/audit/membership-changes`,
  `/admin/stats`.
- **superadmin** (`RequireJWT + RequireSuperadmin`, NOT `RequireActiveTenant`):
  `/superadmin/tenants`, `/superadmin/tenants/:id/users|invites`,
  `/superadmin/plans`, `/superadmin/announcements`, `/superadmin/stats`,
  `/superadmin/audit`, `/superadmin/internal-users`.

### Multi-tenancy

Every tenant-scoped table carries `tenant_id uuid NOT NULL` FK to `tenants`
with `ON DELETE CASCADE` (`department_levels` inherits tenant via
`departments`). Audit logs have **no** FK so they survive hard delete. Hot
indexes lead with `tenant_id`. Handlers pass `claims.TenantID` into services;
services pass `tenant_id` into every sqlc `*Params`. Never query a
tenant-scoped table without it.

`auth.Claims` carries `UserID`, `TenantID`, `UserType`
(`admin`/`worker`/`superadmin`), `LevelIDs`, `SessionID` (`sid`, refresh
family). `RequireActiveTenant` runs one `SELECT status, deleted_at FROM tenants`
per request: `suspended` → 403, deleted/missing → 410.

Tenant lifecycle: `active → suspended → soft-deleted (deleted_at; restorable
within TENANT_DELETE_GRACE_HOURS) → hard-deleted` by `tenants.Sweeper`
(advisory lock `tenants.SweepLockID`). `HardDeleteTenant` is where external
cleanup (object storage etc.) belongs when a domain adds such resources.

The platform tenant (`id 00000000-0000-0000-0000-000000000099`, slug
`PLATFORM_TENANT_SLUG`) holds superadmins; it is never listed and never
deleted. Superadmins have empty `level_ids` and can also hit `/admin/*` for
whichever tenant their JWT names.

### Onboarding

No public signup. `POST /superadmin/tenants` → `superadmin.Service.CreateTenant`
→ `tenants.Service.CreateTenant` (tenant + `General` department + four levels)
→ `invites.Service.Issue(user_type=admin)`. Admin then `POST /admin/invites`
(single) or `/admin/invites/bulk` (CSV, per-row outcomes, cap 1000). A worker
invite with no `level_ids` defaults to General/Employee. `POST
/auth/accept-invite` creates the user, grants levels, returns a session.
Invite accept lives in `internal/invites` (not `auth`) to avoid an import
cycle. Links point at `FRONTEND_BASE_URL/invite?token=<raw>`; only SHA-256 of
tokens is stored.

### Sessions

Login / accept-invite / Google issue an access JWT (`JWT_TTL_SECONDS`, 15m)
plus a rotating httpOnly refresh cookie (`REFRESH_COOKIE_NAME`, `Path=/auth`).
`POST /auth/refresh` rotates; presenting a rotated or revoked token revokes the
whole family (`ErrRefreshReuse`). Password reset revokes all sessions;
password change via `PATCH /me` revokes all except the caller's `sid`; user
soft-delete revokes all. `auth.RefreshSweeper` prunes dead rows.

### Users

Soft delete only (`users.deleted_at`): audit FKs are `RESTRICT`, so rows are
never hard-deleted. Partial unique index `users_email_active_unique` frees an
email for re-invite. `admin.Service.DeleteUser` guards self / last admin /
superadmin; `RestoreUser` honours the grace window.

### Departments + levels

Orthogonal to `user_type`. A level in use (memberships; extend
`CountDepartmentLevelReferences` with your own reference tables) cannot be
deleted. DB trigger `enforce_membership_intra_tenant` blocks cross-tenant
grants (SQLSTATE 23514). `tenants/seed.go` defines the default ladder.

## Conventions

- **SQL first**: write the query in `internal/db/queries/*.sql`, run
  `sqlc generate`, commit the generated code. Never hand-edit `internal/db/sqlc`.
- Column lists in queries that return a whole row must match the table's
  column order so sqlc reuses the model struct (see `tenants.sql`).
- Migrations are append-only; never edit `00000000000001_init.*` after it has
  been applied anywhere. Pair every `.up.sql` with a `.down.sql`.
- Services return sentinel errors (`ErrXNotFound`, `ErrXInUse`); handlers map
  them to status codes. Handlers never touch `pool` directly.
- Request/response JSON uses `snake_case`; stats endpoints that mirror a
  frontend widget use `camelCase` (`admin.TenantStats`, `superadmin.SuperAdminStats`).
- Tests: unit tests use fakes (`email.NoopSender`, in-memory signer); anything
  touching Postgres is gated by `RUN_DB_TESTS=1` and reads `testdb.DSN()`.
- Logging is `slog` JSON; never log tokens, passwords, or raw invite tokens
  outside the noop email sender.
