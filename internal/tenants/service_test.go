package tenants_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
	"github.com/Bold322/bold-back-boiler/internal/testdb"
)

// testDSN is the integration-test database. Override with TEST_DATABASE_URL;
// the default matches docker-compose.yml.
var testDSN = testdb.DSN()

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	pool, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func slugFor(t *testing.T, prefix string) string {
	t.Helper()
	name := strings.ToLower(t.Name())
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, "/", "-")
	return prefix + name
}

func TestCreateTenant_Success(t *testing.T) {
	pool := newPool(t)
	svc := tenants.NewService(pool, sqlc.New(pool))

	slug := slugFor(t, "ct-")
	out, err := svc.CreateTenant(context.Background(), tenants.CreateTenantInput{
		Name:              "Test Org",
		Slug:              slug,
		InitialAdminEmail: "first-admin@example.com",
		InviteTTLHours:    168,
		PublicBaseURL:     "https://app.example.com",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, out.Tenant.ID)
	})

	if out.Tenant.Slug != slug {
		t.Errorf("slug = %q, want %q", out.Tenant.Slug, slug)
	}
	// InviteToken is empty until Phase 9 restores invite issuance.
	if out.InviteToken != "" {
		t.Errorf("InviteToken should be empty until Phase 9, got %q", out.InviteToken)
	}

	// General department + 4 levels were seeded.
	var deptCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM departments WHERE tenant_id = $1 AND name = 'General'`,
		out.Tenant.ID,
	).Scan(&deptCount); err != nil {
		t.Fatalf("count departments: %v", err)
	}
	if deptCount != 1 {
		t.Errorf("expected 1 General department, got %d", deptCount)
	}

	var levelCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM department_levels dl
		 JOIN departments d ON d.id = dl.department_id
		 WHERE d.tenant_id = $1`,
		out.Tenant.ID,
	).Scan(&levelCount); err != nil {
		t.Fatalf("count department levels: %v", err)
	}
	if levelCount != 4 {
		t.Errorf("expected 4 department levels, got %d", levelCount)
	}
}

func TestCreateTenant_RejectsBadSlug(t *testing.T) {
	pool := newPool(t)
	svc := tenants.NewService(pool, sqlc.New(pool))

	_, err := svc.CreateTenant(context.Background(), tenants.CreateTenantInput{
		Name: "x", Slug: "BAD SLUG!", InitialAdminEmail: "a@b.co",
		InviteTTLHours: 168, PublicBaseURL: "https://x",
	})
	if err == nil {
		t.Fatalf("expected error for bad slug")
	}
	if !strings.Contains(err.Error(), "slug") {
		t.Errorf("error should mention slug, got %v", err)
	}
}

func TestLifecycle_SuspendUnsuspend(t *testing.T) {
	pool := newPool(t)
	svc := tenants.NewService(pool, sqlc.New(pool))

	out, err := svc.CreateTenant(context.Background(), tenants.CreateTenantInput{
		Name: "Lifecycle", Slug: slugFor(t, "lc-"),
		InitialAdminEmail: "a@b.co", InviteTTLHours: 168, PublicBaseURL: "https://x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, out.Tenant.ID)
	})

	if err := svc.SuspendTenant(context.Background(), out.Tenant.ID); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	var status string
	_ = pool.QueryRow(context.Background(), `SELECT status FROM tenants WHERE id = $1`, out.Tenant.ID).Scan(&status)
	if status != "suspended" {
		t.Errorf("status = %q, want suspended", status)
	}

	if err := svc.UnsuspendTenant(context.Background(), out.Tenant.ID); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}
	_ = pool.QueryRow(context.Background(), `SELECT status FROM tenants WHERE id = $1`, out.Tenant.ID).Scan(&status)
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
}

func TestLifecycle_SoftDeleteRestore(t *testing.T) {
	pool := newPool(t)
	svc := tenants.NewService(pool, sqlc.New(pool))

	out, err := svc.CreateTenant(context.Background(), tenants.CreateTenantInput{
		Name: "Restore", Slug: slugFor(t, "rs-"),
		InitialAdminEmail: "a@b.co", InviteTTLHours: 168, PublicBaseURL: "https://x",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, out.Tenant.ID)
	})

	if err := svc.SoftDeleteTenant(context.Background(), out.Tenant.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if err := svc.RestoreTenant(context.Background(), out.Tenant.ID, 168); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var status string
	var deletedAt *string
	_ = pool.QueryRow(context.Background(),
		`SELECT status, deleted_at::text FROM tenants WHERE id = $1`,
		out.Tenant.ID,
	).Scan(&status, &deletedAt)
	if status != "active" || deletedAt != nil {
		t.Errorf("status=%q deleted_at=%v after restore", status, deletedAt)
	}
}
