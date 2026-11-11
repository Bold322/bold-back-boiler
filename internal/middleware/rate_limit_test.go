package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

func TestRateLimit_AllowsUpToBudget(t *testing.T) {
	rl := middleware.NewRateLimit(2, time.Minute)

	e := echo.New()
	handler := rl.Middleware()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "1.2.3.4:5678"
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		_ = handler(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i, rec.Code)
		}
	}
}

func TestRateLimit_RejectsOverBudget(t *testing.T) {
	rl := middleware.NewRateLimit(2, time.Minute)
	e := echo.New()
	handler := rl.Middleware()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "1.2.3.4:5678"
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		_ = handler(c)
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = handler(c)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

// byUserCall runs one request through the handler with the given user's
// claims set, always from the same IP so the tests prove the key is the user.
func byUserCall(e *echo.Echo, handler echo.HandlerFunc, userID uuid.UUID) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("auth.claims", &auth.Claims{UserID: userID, TenantID: uuid.New(), UserType: "worker"})
	_ = handler(c)
	return rec
}

func TestRateLimitByUser_IndependentBudgetsPerUser(t *testing.T) {
	rl := middleware.NewRateLimit(1, time.Minute)
	e := echo.New()
	handler := rl.ByUser()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	for _, id := range []uuid.UUID{uuid.New(), uuid.New()} {
		if rec := byUserCall(e, handler, id); rec.Code != http.StatusOK {
			t.Fatalf("user %s from shared IP: expected 200, got %d", id, rec.Code)
		}
	}
}

func TestRateLimitByUser_RejectsOverBudget(t *testing.T) {
	rl := middleware.NewRateLimit(2, time.Minute)
	e := echo.New()
	handler := rl.ByUser()(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	user := uuid.New()
	byUserCall(e, handler, user)
	byUserCall(e, handler, user)
	if rec := byUserCall(e, handler, user); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}
