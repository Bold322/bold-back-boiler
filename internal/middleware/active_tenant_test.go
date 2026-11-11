package middleware_test

import (
	"context"
	"github.com/Bold322/bold-back-boiler/internal/testdb"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

// testDSN is the integration-test database. Override with TEST_DATABASE_URL;
// the default matches docker-compose.yml.
var testDSN = testdb.DSN()

func setupClaims(c echo.Context, tenantID uuid.UUID) {
	c.Set("auth.claims", &auth.Claims{
		UserID:   uuid.New(),
		TenantID: tenantID,
		UserType: "worker",
	})
}

func TestRequireActiveTenant_AllowsActive(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `
        INSERT INTO tenants (name, slug) VALUES ('Active Test', 'active-test-' || substr(md5(random()::text), 1, 8))
        RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setupClaims(c, tenantID)

	handler := middleware.RequireActiveTenant(pool)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireActiveTenant_RejectsSuspended(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `
        INSERT INTO tenants (name, slug, status)
        VALUES ('Suspended Test', 'susp-' || substr(md5(random()::text), 1, 8), 'suspended')
        RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setupClaims(c, tenantID)

	handler := middleware.RequireActiveTenant(pool)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	_ = handler(c)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireActiveTenant_RejectsSoftDeleted(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `
        INSERT INTO tenants (name, slug, status, deleted_at)
        VALUES ('Deleted Test', 'del-' || substr(md5(random()::text), 1, 8), 'suspended', now())
        RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenantID)
	})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setupClaims(c, tenantID)

	handler := middleware.RequireActiveTenant(pool)(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	_ = handler(c)
	if rec.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d body=%s", rec.Code, rec.Body.String())
	}
}
