package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

func TestRequireSuperadmin_AllowsSuperadminClaim(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("auth.claims", &auth.Claims{
		UserID: uuid.New(), TenantID: uuid.New(), UserType: "superadmin",
	})

	handler := middleware.RequireSuperadmin()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	if err := handler(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRequireSuperadmin_RejectsAdmin(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("auth.claims", &auth.Claims{
		UserID: uuid.New(), TenantID: uuid.New(), UserType: "admin",
	})

	handler := middleware.RequireSuperadmin()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})
	_ = handler(c)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}
