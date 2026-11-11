# bold-back-boiler

Go backend boilerplate for a **multi-tenant SaaS API**: tenants, invite-only
onboarding, JWT access tokens with rotating refresh cookies, tenant admins and
a platform superadmin tier, departments/levels for fine-grained access, email
via SES (or a log-only sender), optional Google sign-in. Start a new product
by adding your domain modules on top; everything generic is already here and
tested.

> **MN / Товч танилцуулга.** Энэ бол ямар ч бүтээгдэхүүний backend-ийг эхлүүлэхэд
> зориулсан Go суурь (boilerplate). Multi-tenant бүтэц, нэвтрэлт (JWT + refresh
> cookie), урилгаар бүртгэх, admin / superadmin эрх, хэлтэс-түвшин, имэйл,
> Google нэвтрэлт бүгд бэлэн. Та зөвхөн өөрийн домэйн модулиа
> (`internal/<module>` + SQL query + migration + route) нэмээд ажиллана.
> Эхлэх: `cp .env.example .env` → `make db-up` → `make run`.

## Stack

| Layer | Choice |
|-------|--------|
| HTTP | [Echo v4](https://echo.labstack.com/) |
| DB | PostgreSQL 17, [pgx v5](https://github.com/jackc/pgx), [sqlc](https://sqlc.dev) for typed queries |
| Migrations | [golang-migrate](https://github.com/golang-migrate/migrate), embedded in the binary, applied at startup under an advisory lock |
| Auth | HS256 JWT (`golang-jwt/v5`), bcrypt passwords, rotating refresh tokens, optional Google OAuth |
| Email | AWS SES v2 or a log-only noop sender |
| Config | Environment variables only, validated at startup (`internal/config`) |
| Build | `Makefile`, distroless `Dockerfile`, GitHub Actions CI with a Postgres service |

## Features

- **Tenants** with lifecycle `active → suspended → soft-deleted → hard-deleted`
  (grace window + background sweeper, FK cascade). Tier + feature-flag JSON on
  every tenant, editable by superadmins.
- **Users** scoped to a tenant with `user_type` `admin` / `worker` /
  `superadmin`, soft delete + restore, globally unique active email so login
  needs only email + password.
- **Sessions**: short-lived access JWT (15 min default) + httpOnly rotating
  refresh cookie with reuse detection, logout, password reset by email,
  password change that revokes other sessions.
- **Invite-only onboarding**: no public signup. Superadmin creates a tenant and
  the first admin invite; admins invite members (single or CSV bulk), list,
  revoke, resend. Accepting an invite creates the user and logs them in.
- **Departments + levels**: each tenant has an ordered ladder per department
  (seeded `General`: Employee → CEO). Membership grants are audit-logged and a
  DB trigger prevents cross-tenant grants. Use `level_ids` from the JWT claims
  as the hook for your own row-level access control.
- **Admin API** (`/admin/*`): members, user type, memberships, departments,
  levels, invites, audit feed, overview stats.
- **Superadmin API** (`/superadmin/*`): tenant CRUD + lifecycle, cross-tenant
  member management, plans catalog, announcements, platform stats, audit.
- **Middleware**: `RequireJWT`, `RequireActiveTenant`, `RequireAdmin`,
  `RequireSuperadmin`, per-IP / per-user fixed-window rate limit, credentialed
  CORS from an allowlist.

## Quickstart

```bash
git clone https://github.com/Bold322/bold-back-boiler.git my-api && cd my-api
cp .env.example .env            # set JWT_SECRET (openssl rand -base64 48)
make db-up                      # Postgres 17 on localhost:15432 (docker compose)
make run                        # builds ./bin/server, runs migrations, listens on :3000
curl localhost:3000/health      # {"status":"ok"}
```

Bootstrap the first superadmin either from env (set `SUPERADMIN_EMAIL`,
`SUPERADMIN_PASSWORD`, `SUPERADMIN_DISPLAY_NAME` before `make run`) or with
the CLI:

```bash
make superadmin EMAIL=root@example.com NAME=Root PASS=change-me-now
```

Create a tenant and its first admin, then accept the invite:

```bash
TOK=$(curl -s -X POST localhost:3000/auth/login -H 'content-type: application/json' \
  -d '{"email":"root@example.com","password":"change-me-now"}' | jq -r .token)

curl -s -X POST localhost:3000/superadmin/tenants -H "authorization: Bearer $TOK" \
  -H 'content-type: application/json' \
  -d '{"name":"Acme","slug":"acme","initial_admin_email":"admin@acme.test"}'
# -> {"tenant_id":"…","invite_token":"…","accept_url":"http://localhost:4200/invite?token=…"}

curl -s -X POST localhost:3000/auth/accept-invite -H 'content-type: application/json' \
  -d '{"token":"<invite_token>","password":"adminpass123","display_name":"Acme Admin"}'
# -> {"token":"<access jwt>", "user":{…}}  (+ refresh cookie)
```

With `EMAIL_DELIVERY=off` (default) invite and reset emails are logged, not
sent; the raw invite token is also returned in the API response.

The matching frontend boilerplate is
[bold-front-boiler](https://github.com/Bold322/bold-front-boiler).

## Project layout

```
cmd/
  server/            main binary: config → pool → migrations → services → router → sweepers
  seed/              CLI: create a tenant + first-admin invite
  superadmin-create/ CLI: create a platform superadmin
internal/
  config/            env parsing + validation (fail fast)
  db/                pgx pool; queries/*.sql → sqlc → sqlc/ (generated, committed)
  auth/              login, JWT signer, refresh tokens, password reset, Google OAuth, /me
  middleware/        RequireJWT, RequireActiveTenant, RequireAdmin, RequireSuperadmin, RateLimit
  tenants/           tenant service + default department seed + hard-delete sweeper
  invites/           issue / bulk / list / revoke / resend / accept
  admin/             per-tenant admin: users, departments, levels, memberships, audit, stats
  superadmin/        platform tier: tenant lifecycle, cross-tenant users, plans, announcements, stats
  preferences/       per-user settings row
  email/             Sender interface, SES + noop implementations, templates
  server/            Echo router: the canonical route map
  testdb/            TEST_DATABASE_URL helper for integration tests
migrations/          embedded SQL, 00000000000001_init.{up,down}.sql is the platform baseline
```

## Adding a domain module

Every feature follows the same path. Example: `projects`.

1. **Migration** – `migrations/20260601000000_projects.up.sql` / `.down.sql`.
   Tenant-scoped tables carry `tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE`
   and lead their indexes with `tenant_id`. For level-based access add
   `allowed_level_ids uuid[]` and extend `CountDepartmentLevelReferences` /
   `CountDepartmentReferences` so a level in use cannot be deleted.
2. **Queries** – `internal/db/queries/projects.sql` with `-- name: ListProjects :many`
   etc. Every query takes `tenant_id`. Run `make sqlc`.
3. **Service** – `internal/projects/service.go`: business rules, takes
   `(pool, *sqlc.Queries)`, returns typed errors (`ErrProjectNotFound`).
4. **Handler** – `internal/projects/handler.go`: Echo handlers; read
   `middleware.MustClaims(c)` for `TenantID`, `UserID`, `LevelIDs`, `IsAdmin()`.
5. **Routes** – register in `internal/server/server.go` under the right group
   (`authed`, `adminGroup`, `superGroup`). Wire the service in `cmd/server/main.go`
   and pass it through `server.Deps`.
6. **Tests** – unit tests next to the code; DB tests gated by `RUN_DB_TESTS=1`
   using `testdb.DSN()`.

## Configuration

All config is env (see `.env.example` for every variable with comments).
Required: `POSTGRES_DB/USER/PASSWORD`, `JWT_SECRET` (≥ 32 chars),
`DEFAULT_TENANT_NAME/SLUG`. Notable optional groups: `REFRESH_*` (session
cookie), `GOOGLE_OAUTH_*` (all three or none), `EMAIL_DELIVERY=on` + `SES_*`,
`SUPERADMIN_*` bootstrap, `TENANT_SWEEP_*`, `CORS_ALLOWED_ORIGINS`.

## Testing

```bash
make test               # unit tests (DB tests skip)
make test-integration   # RUN_DB_TESTS=1 against docker-compose Postgres
TEST_DATABASE_URL=postgres://me@localhost:5432/scratch?sslmode=disable make test-integration
make vet && gofmt -l .
```

Integration tests reset / truncate shared tables, so point them at a
throwaway database. CI (`.github/workflows/ci.yml`) runs gofmt, vet, build,
checks that `sqlc generate` is committed, runs the full test suite against a
Postgres service, and builds the Docker image.

## Deploying

`Dockerfile` produces a distroless static image of `cmd/server`. Provide the
env vars from `.env.example`, set `POSTGRES_SSLMODE=require`,
`REFRESH_COOKIE_SAMESITE=none` when the SPA lives on another domain, and
`FRONTEND_BASE_URL` + `CORS_ALLOWED_ORIGINS` to the real origins. Migrations
run on startup and are safe across replicas (advisory lock).

## License

MIT
