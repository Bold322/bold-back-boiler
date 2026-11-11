package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
)

const testLoginPassword = "correct horse battery staple"

// newTestAuthHandler seeds a fresh tenant + a worker user with a real bcrypt
// password hash and wires a Handler over a real Service (with a working
// refresh-cookie config). The shared seedUser helper in refresh_test.go
// stores an empty password_hash, which never verifies against any password,
// so a Login-handler test needs its own seed.
func newTestAuthHandler(t *testing.T) (h *auth.Handler, userEmail, password string) {
	t.Helper()
	pool := newTestPool(t)
	ctx := context.Background()

	hash, err := auth.HashPassword(testLoginPassword)
	require.NoError(t, err)

	var tid, userID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('Auth Login', 'auth-login-' || substr(md5(random()::text), 1, 8)) RETURNING id`,
	).Scan(&tid))

	userEmail = "login-" + strings.ToLower(uuid.New().String()[:8]) + "@x.com"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
         VALUES ($1, $2, $3, 'Worker', 'worker') RETURNING id`, tid, userEmail, hash,
	).Scan(&userID))

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tid)
	})

	signer := auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900)
	svc := auth.NewService(pool, sqlc.New(pool), signer, email.NoopSender{}, "http://localhost:4200", auth.RefreshConfig{
		CookieName:  "app_refresh",
		IdleTTL:     14 * 24 * time.Hour,
		AbsoluteTTL: 90 * 24 * time.Hour,
	})
	return auth.NewHandler(svc), userEmail, testLoginPassword
}

// newTestAuthHandlerWithSession logs in once against a real Handler and
// returns that handler plus the refresh cookie the login response set, so
// refresh-rotation tests can present a genuine, DB-backed refresh token.
func newTestAuthHandlerWithSession(t *testing.T) (h *auth.Handler, refreshCookie *http.Cookie) {
	t.Helper()
	h, email, password := newTestAuthHandler(t)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Login(c))
	require.Equal(t, http.StatusOK, rec.Code)

	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "app_refresh" {
			refreshCookie = ck
		}
	}
	require.NotNil(t, refreshCookie, "expected login to set a refresh cookie")
	return h, refreshCookie
}

func TestRefreshHandlerRequiresCSRFHeader(t *testing.T) {
	h := auth.NewHandler(auth.NewService(nil, nil, auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900), email.NoopSender{}, "http://localhost:4200", auth.RefreshConfig{
		CookieName:  "app_refresh",
		IdleTTL:     14 * 24 * time.Hour,
		AbsoluteTTL: 90 * 24 * time.Hour,
	}))
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Refresh(c))
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRefreshHandlerRotates(t *testing.T) {
	skipIfNoDB(t)
	h, refreshCookie := newTestAuthHandlerWithSession(t)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("X-Refresh-Request", "1")
	req.AddCookie(refreshCookie)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Refresh(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"token"`)

	var rotated bool
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "app_refresh" && ck.Value != "" && ck.Value != refreshCookie.Value {
			rotated = true
		}
	}
	require.True(t, rotated)
}

func TestLoginSetsRefreshCookie(t *testing.T) {
	skipIfNoDB(t)
	// Arrange a Handler over a real Service + seeded user (reuse the Task 4
	// test seed to create a user with a known password).
	h, email, password := newTestAuthHandler(t)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Login(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var found bool
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "app_refresh" {
			found = true
			require.True(t, ck.HttpOnly)
			require.NotEmpty(t, ck.Value)
			require.Equal(t, "/auth", ck.Path)
		}
	}
	require.True(t, found, "expected refresh cookie on login")
}

func TestLogoutHandlerRequiresCSRFHeader(t *testing.T) {
	h := auth.NewHandler(auth.NewService(nil, nil, auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900), email.NoopSender{}, "http://localhost:4200", auth.RefreshConfig{
		CookieName:  "app_refresh",
		IdleTTL:     14 * 24 * time.Hour,
		AbsoluteTTL: 90 * 24 * time.Hour,
	}))
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, h.Logout(c))
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// TestLogoutClearsCookieAndRevokes proves logout both clears the refresh
// cookie in the response and kills the presented session server-side: the
// same refresh token, resubmitted to the real /auth/refresh handler, must be
// rejected instead of rotating.
func TestLogoutClearsCookieAndRevokes(t *testing.T) {
	skipIfNoDB(t)
	h, refreshCookie := newTestAuthHandlerWithSession(t)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("X-Refresh-Request", "1")
	req.AddCookie(refreshCookie)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Logout(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var cleared bool
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "app_refresh" {
			cleared = true
			require.True(t, ck.MaxAge < 0)
		}
	}
	require.True(t, cleared, "expected logout to clear the refresh cookie")

	// The revoked token can no longer refresh, via the real handler route.
	refreshReq := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	refreshReq.Header.Set("X-Refresh-Request", "1")
	refreshReq.AddCookie(refreshCookie)
	refreshRec := httptest.NewRecorder()
	refreshC := e.NewContext(refreshReq, refreshRec)
	require.NoError(t, h.Refresh(refreshC))
	require.Equal(t, http.StatusUnauthorized, refreshRec.Code)
}
