package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

func TestRequireJWT_PassesValidTokenAndPopulatesContext(t *testing.T) {
	signer := auth.NewJWTSigner("super-long-test-secret-string-32-chars+", "test", 60)
	uid := uuid.New()
	tenantID := uuid.New()
	levelID := uuid.New()
	tok, err := signer.Sign(uid, tenantID, "admin", []uuid.UUID{levelID})
	require.NoError(t, err)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	var seen *auth.Claims
	handler := middleware.RequireJWT(signer)(func(c echo.Context) error {
		seen = middleware.MustClaims(c)
		return c.NoContent(http.StatusOK)
	})

	require.NoError(t, handler(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, seen)
	require.Equal(t, uid, seen.UserID)
	require.Equal(t, tenantID, seen.TenantID)
	require.True(t, seen.IsAdmin())
	require.Equal(t, []uuid.UUID{levelID}, seen.LevelIDs)
}

func TestRequireJWT_Returns401WithoutHeader(t *testing.T) {
	signer := auth.NewJWTSigner("super-long-test-secret-string-32-chars+", "test", 60)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()

	handler := middleware.RequireJWT(signer)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	require.NoError(t, handler(e.NewContext(req, rec)))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireJWT_Returns401WhenTokenMalformed(t *testing.T) {
	signer := auth.NewJWTSigner("super-long-test-secret-string-32-chars+", "test", 60)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()

	handler := middleware.RequireJWT(signer)(func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})

	require.NoError(t, handler(e.NewContext(req, rec)))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
