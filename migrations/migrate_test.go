package migrations_test

import (
	"context"
	"github.com/Bold322/bold-back-boiler/internal/testdb"
	"os"
	"testing"

	"github.com/Bold322/bold-back-boiler/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testDSN is the integration-test database. Override with TEST_DATABASE_URL;
// the default matches docker-compose.yml.
var testDSN = testdb.DSN()

func TestUpFromFreshDB(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	// Drop and recreate the public schema for a fully clean start.
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	if err := migrations.Up(testDSN, "Test Default", "test-default", "platform"); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Verify the default tenant exists.
	var slug string
	if err := pool.QueryRow(ctx, `SELECT slug FROM tenants WHERE slug = 'test-default'`).Scan(&slug); err != nil {
		t.Fatalf("default tenant missing: %v", err)
	}
	if slug != "test-default" {
		t.Fatalf("slug mismatch: %q", slug)
	}
}

func TestUp_SchemaAppliesCleanly(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	// Drop and recreate the public schema for a fully clean start.
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	require.NoError(t, migrations.Up(testDSN, "Default Tenant", "default", "platform"))

	// 1. Platform tenant exists with the fixed uuid.
	var platformID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM tenants WHERE slug = 'platform'`).Scan(&platformID))
	require.Equal(t, "00000000-0000-0000-0000-000000000099", platformID.String())

	// 2. General department in the default tenant has exactly four levels.
	var levelCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM department_levels dl
		JOIN departments d ON d.id = dl.department_id
		JOIN tenants t ON t.id = d.tenant_id
		WHERE t.slug = 'default' AND d.name = 'General'
	`).Scan(&levelCount))
	require.Equal(t, 4, levelCount)

	// 3. Every platform table exists.
	for _, tbl := range []string{
		"tenants", "users", "departments", "department_levels", "user_department_memberships",
		"membership_audit_log", "department_catalog_audit_log", "invites", "refresh_tokens",
		"password_reset_tokens", "user_preferences", "tenant_plans", "system_announcements",
	} {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_name = $1 AND table_schema = 'public'
			)`, tbl).Scan(&exists))
		require.True(t, exists, "table %s missing", tbl)
	}

	// 4. The four plan rows are seeded.
	var plans int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tenant_plans`).Scan(&plans))
	require.Equal(t, 4, plans)

	// 5. Up is idempotent (ErrNoChange is swallowed).
	require.NoError(t, migrations.Up(testDSN, "Default Tenant", "default", "platform"))
}
