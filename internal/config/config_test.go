package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/config"
)

// setBaseEnv sets every required env var to a working value so individual
// tests can mutate just the variable under test.
func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("POSTGRES_HOST", "localhost")
	t.Setenv("POSTGRES_PORT", "5432")
	t.Setenv("POSTGRES_DB", "app")
	t.Setenv("POSTGRES_USER", "app")
	t.Setenv("POSTGRES_PASSWORD", "x")
	t.Setenv("JWT_SECRET", "this-is-a-long-secret-string-32+chars")
	t.Setenv("JWT_ISSUER", "test-issuer")
	t.Setenv("JWT_TTL_SECONDS", "3600")
	t.Setenv("API_HOST", "0.0.0.0")
	t.Setenv("API_PORT", "3000")
	t.Setenv("DEFAULT_TENANT_NAME", "Test Tenant")
	t.Setenv("DEFAULT_TENANT_SLUG", "test")
	// Isolate from an ambient .env exported by make.
	t.Setenv("SES_REGION", "")
	t.Setenv("REFRESH_COOKIE_NAME", "")
	t.Setenv("FRONTEND_BASE_URL", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URL", "")
	t.Setenv("SUPERADMIN_EMAIL", "")
	t.Setenv("SUPERADMIN_PASSWORD", "")
	t.Setenv("SUPERADMIN_DISPLAY_NAME", "")
	t.Setenv("EMAIL_DELIVERY", "")
}

func TestLoad_ReturnsErrorWhenJWTSecretMissing(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("JWT_SECRET", "")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "JWT_SECRET")
	require.Contains(t, err.Error(), "32 characters")
}

func TestLoad_ReturnsErrorWhenJWTSecretTooShort(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("JWT_SECRET", "too-short")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "JWT_SECRET")
}

func TestLoad_ReturnsErrorWhenPostgresMissing(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("POSTGRES_DB", "")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "POSTGRES_DB")
}

func TestLoad_ReturnsErrorWhenDefaultTenantMissing(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("DEFAULT_TENANT_SLUG", "")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "DEFAULT_TENANT_NAME and DEFAULT_TENANT_SLUG")
}

func TestLoad_PopulatesAllFieldsWhenEnvSet(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("POSTGRES_HOST", "db.example")
	t.Setenv("POSTGRES_PORT", "5433")
	t.Setenv("POSTGRES_SSLMODE", "require")
	t.Setenv("JWT_TTL_SECONDS", "1800")
	t.Setenv("API_PORT", "9090")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "db.example", cfg.Postgres.Host)
	require.Equal(t, 5433, cfg.Postgres.Port)
	require.Equal(t, "require", cfg.Postgres.SSLMode)
	require.Equal(t, 1800, cfg.JWT.TTLSeconds)
	require.Equal(t, 9090, cfg.API.Port)
	require.Equal(t, "platform", cfg.DefaultTenant.PlatformSlug)
}

func TestLoad_TenantLifecycleHasDefaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 168, cfg.Tenants.DeleteGraceHours)
	require.True(t, cfg.Tenants.SweepEnabled)
	require.Equal(t, 60, cfg.Tenants.SweepIntervalMinutes)
	require.Equal(t, 50, cfg.Tenants.SweepBatchSize)
	require.Equal(t, 168, cfg.Invites.TTLHours)
}

func TestLoad_GoogleOAuthIsAllOrNothing(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "id")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "GOOGLE_OAUTH_CLIENT_ID")

	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("GOOGLE_OAUTH_REDIRECT_URL", "http://localhost:3000/auth/google/callback")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.True(t, cfg.Google.Enabled())
}

func TestLoad_EmailDeliveryOnRequiresSES(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("EMAIL_DELIVERY", "on")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "SES_FROM_ADDRESS")
	require.Contains(t, err.Error(), "PUBLIC_BASE_URL")
}

func TestLoad_SuperadminBootstrapIsAllOrNothing(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("SUPERADMIN_EMAIL", "Root@Example.com")

	_, err := config.Load()
	require.Error(t, err)

	t.Setenv("SUPERADMIN_PASSWORD", "longenough")
	t.Setenv("SUPERADMIN_DISPLAY_NAME", "Root")
	cfg, err := config.Load()
	require.NoError(t, err)
	require.True(t, cfg.Superadmin.Enabled())
	require.Equal(t, "root@example.com", cfg.Superadmin.Email)
}

func TestLoadRefreshDefaults(t *testing.T) {
	setBaseEnv(t)
	// setBaseEnv pins JWT_TTL_SECONDS=3600 for the other tests; clear it here
	// so this test observes the actual default.
	t.Setenv("JWT_TTL_SECONDS", "")
	t.Setenv("FRONTEND_BASE_URL", "https://app.example.com")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://admin.example.com, https://app.example.com")

	cfg, err := config.Load()
	require.NoError(t, err)

	require.Equal(t, 900, cfg.JWT.TTLSeconds)
	require.Equal(t, 336, cfg.Refresh.IdleHours)
	require.Equal(t, 2160, cfg.Refresh.AbsoluteHours)
	require.Equal(t, "app_refresh", cfg.Refresh.CookieName)
	require.Equal(t, "lax", cfg.Refresh.CookieSameSite)
	require.True(t, cfg.Refresh.SweepEnabled)
	// FRONTEND_BASE_URL is always included; extras are de-duplicated.
	require.ElementsMatch(t,
		[]string{"https://app.example.com", "https://admin.example.com"},
		cfg.CORSAllowedOrigins)
}

func TestLoad_RejectsBadSameSite(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("REFRESH_COOKIE_SAMESITE", "weird")
	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "REFRESH_COOKIE_SAMESITE")
}
