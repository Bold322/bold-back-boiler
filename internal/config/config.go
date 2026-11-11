package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type DefaultTenantConfig struct {
	Name         string
	Slug         string
	PlatformSlug string // slug of the synthetic platform tenant that holds superadmin users
}

type Config struct {
	Postgres      PostgresConfig
	JWT           JWTConfig
	API           APIConfig
	Tenants       TenantsConfig
	DefaultTenant DefaultTenantConfig
	Email         EmailConfig
	Invites       InvitesConfig
	Google        GoogleOAuthConfig
	Superadmin    SuperadminBootstrapConfig
	// FrontendBaseURL is where the Google OAuth callback redirects the
	// browser back to after issuing (or failing to issue) a JWT. Also the
	// origin the SPA is served from. Default targets local Vite dev.
	FrontendBaseURL    string
	Refresh            RefreshConfig
	CORSAllowedOrigins []string
	LogLevel           string
}

// GoogleOAuthConfig holds the Google "Sign in with Google" credentials for
// the backend authorization-code flow. All three fields are set together or
// not at all; when unset the /auth/google/* routes are not registered and the
// product runs password-only. Google sign-in authenticates existing users
// only (no JIT provisioning) to preserve the invite-only invariant.
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	// RedirectURL must exactly match an Authorized redirect URI in the
	// Google Cloud OAuth client, e.g.
	// http://localhost:3000/auth/google/callback in dev.
	RedirectURL string
}

// Enabled reports whether Google sign-in is configured. Partial config is a
// load-time error, so Enabled is all-or-nothing.
func (g GoogleOAuthConfig) Enabled() bool {
	return g.ClientID != "" && g.ClientSecret != "" && g.RedirectURL != ""
}

// SuperadminBootstrapConfig optionally seeds the first platform superadmin on
// startup so a fresh deployment is usable without running cmd/superadmin-create
// by hand. Idempotent: if a superadmin with this email already exists the
// bootstrap is a no-op (the env password does NOT overwrite it). All three
// fields are set together or none (partial config is a load-time error).
// SECURITY: the password lives in the environment; rotate it after first
// login via PATCH /me.
type SuperadminBootstrapConfig struct {
	Email       string
	Password    string
	DisplayName string
}

// Enabled reports whether the startup superadmin bootstrap should run.
func (s SuperadminBootstrapConfig) Enabled() bool {
	return s.Email != "" && s.Password != "" && s.DisplayName != ""
}

// InvitesConfig controls invite-token TTL for admin-issued worker invites.
// Default 168 hours (7 days) gives recipients a working week to accept.
type InvitesConfig struct {
	TTLHours int
}

// TenantsConfig drives the tenant lifecycle sweeper.
// Default grace is 168 hours (7 days) so an accidental tenant delete is
// recoverable for a working week.
type TenantsConfig struct {
	DeleteGraceHours     int
	SweepEnabled         bool
	SweepIntervalMinutes int
	SweepBatchSize       int
}

// EmailConfig controls invite-email delivery. Delivery="off" routes invites
// through the noop sender (logs only); "on" requires SES credentials and a
// reachable public base URL so accept-invite links resolve.
type EmailConfig struct {
	Delivery      string
	SESRegion     string
	SESFromAddr   string
	PublicBaseURL string
}

type PostgresConfig struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	// SSLMode is the libpq sslmode (disable, allow, prefer, require, verify-ca,
	// verify-full). Empty means "disable" so dev tooling that constructs
	// PostgresConfig manually keeps the historical behavior.
	SSLMode string
}

func (p PostgresConfig) DSN() string {
	mode := p.SSLMode
	if mode == "" {
		mode = "disable"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(p.User), url.QueryEscape(p.Password),
		p.Host, p.Port, p.Database, mode)
}

type JWTConfig struct {
	Secret string
	// Issuer is the JWT `iss` claim. Signer and verifier must agree.
	// Default works for single-deployment dev. Override JWT_ISSUER per
	// environment if multiple deployments share token verification.
	Issuer     string
	TTLSeconds int
}

// RefreshConfig controls the refresh-token session-renewal flow and its cookie.
type RefreshConfig struct {
	IdleHours            int
	AbsoluteHours        int
	CookieName           string
	CookieSameSite       string // lax | none | strict
	CookieDomain         string
	SweepEnabled         bool
	SweepIntervalMinutes int
	RetentionHours       int
}

type APIConfig struct {
	// Host is the bind address. Default 0.0.0.0 binds all interfaces,
	// which is correct for Docker and dev. Override API_HOST in hardened
	// environments to restrict the surface.
	Host string
	Port int
}

func Load() (*Config, error) {
	var errs []string

	cfg := &Config{
		Postgres: PostgresConfig{
			Host:     getenvDefault("POSTGRES_HOST", "localhost"),
			Database: requireEnv("POSTGRES_DB", &errs),
			User:     requireEnv("POSTGRES_USER", &errs),
			Password: requireEnv("POSTGRES_PASSWORD", &errs),
			SSLMode:  getenvDefault("POSTGRES_SSLMODE", "disable"),
		},
		JWT: JWTConfig{
			Secret: os.Getenv("JWT_SECRET"),
			Issuer: getenvDefault("JWT_ISSUER", "bold-back-boiler"),
		},
		API: APIConfig{
			Host: getenvDefault("API_HOST", "0.0.0.0"),
		},
		LogLevel: getenvDefault("LOG_LEVEL", "info"),
	}

	cfg.Postgres.Port = parseIntDefault("POSTGRES_PORT", 5432, &errs)
	cfg.JWT.TTLSeconds = parseIntDefault("JWT_TTL_SECONDS", 900, &errs)
	cfg.API.Port = parseIntDefault("API_PORT", 3000, &errs)

	cfg.Tenants = TenantsConfig{
		DeleteGraceHours:     parseIntDefault("TENANT_DELETE_GRACE_HOURS", 168, &errs),
		SweepEnabled:         parseBoolDefault("TENANT_SWEEP_ENABLED", true, &errs),
		SweepIntervalMinutes: parseIntDefault("TENANT_SWEEP_INTERVAL_MINUTES", 60, &errs),
		SweepBatchSize:       parseIntDefault("TENANT_SWEEP_BATCH_SIZE", 50, &errs),
	}

	cfg.Invites = InvitesConfig{
		TTLHours: parseIntDefault("INVITE_TTL_HOURS", 168, &errs),
	}

	cfg.DefaultTenant = DefaultTenantConfig{
		Name:         os.Getenv("DEFAULT_TENANT_NAME"),
		Slug:         os.Getenv("DEFAULT_TENANT_SLUG"),
		PlatformSlug: getenvDefault("PLATFORM_TENANT_SLUG", "platform"),
	}
	if cfg.DefaultTenant.Name == "" || cfg.DefaultTenant.Slug == "" {
		errs = append(errs, "DEFAULT_TENANT_NAME and DEFAULT_TENANT_SLUG are required")
	}

	cfg.Email = EmailConfig{
		Delivery:      getenvDefault("EMAIL_DELIVERY", "off"),
		SESRegion:     getenvDefault("SES_REGION", "us-east-1"),
		SESFromAddr:   os.Getenv("SES_FROM_ADDRESS"),
		PublicBaseURL: os.Getenv("PUBLIC_BASE_URL"),
	}
	if cfg.Email.Delivery == "on" {
		if cfg.Email.SESFromAddr == "" {
			errs = append(errs, "SES_FROM_ADDRESS is required when EMAIL_DELIVERY=on")
		}
		if cfg.Email.PublicBaseURL == "" {
			errs = append(errs, "PUBLIC_BASE_URL is required when EMAIL_DELIVERY=on")
		}
	} else if cfg.Email.Delivery != "off" {
		errs = append(errs, fmt.Sprintf("EMAIL_DELIVERY must be 'on' or 'off', got %q", cfg.Email.Delivery))
	}

	cfg.Google = GoogleOAuthConfig{
		ClientID:     os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("GOOGLE_OAUTH_REDIRECT_URL"),
	}
	cfg.FrontendBaseURL = strings.TrimRight(
		getenvDefault("FRONTEND_BASE_URL", "http://localhost:4200"), "/")

	cfg.Refresh = RefreshConfig{
		IdleHours:            parseIntDefault("REFRESH_TOKEN_IDLE_HOURS", 336, &errs),
		AbsoluteHours:        parseIntDefault("REFRESH_TOKEN_ABSOLUTE_HOURS", 2160, &errs),
		CookieName:           getenvDefault("REFRESH_COOKIE_NAME", "app_refresh"),
		CookieSameSite:       strings.ToLower(getenvDefault("REFRESH_COOKIE_SAMESITE", "lax")),
		CookieDomain:         getenvDefault("REFRESH_COOKIE_DOMAIN", ""),
		SweepEnabled:         parseBoolDefault("REFRESH_SWEEP_ENABLED", true, &errs),
		SweepIntervalMinutes: parseIntDefault("REFRESH_SWEEP_INTERVAL_MINUTES", 60, &errs),
		RetentionHours:       parseIntDefault("REFRESH_TOKEN_RETENTION_HOURS", 720, &errs),
	}
	switch cfg.Refresh.CookieSameSite {
	case "lax", "none", "strict":
	default:
		errs = append(errs, "REFRESH_COOKIE_SAMESITE must be one of lax|none|strict")
	}
	cfg.CORSAllowedOrigins = buildCORSOrigins(cfg.FrontendBaseURL, os.Getenv("CORS_ALLOWED_ORIGINS"))
	{
		set := 0
		for _, v := range []string{cfg.Google.ClientID, cfg.Google.ClientSecret, cfg.Google.RedirectURL} {
			if v != "" {
				set++
			}
		}
		if set != 0 && set != 3 {
			errs = append(errs, "GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, and GOOGLE_OAUTH_REDIRECT_URL must all be set together (or all unset to disable Google sign-in)")
		}
	}

	cfg.Superadmin = SuperadminBootstrapConfig{
		Email:       strings.ToLower(strings.TrimSpace(os.Getenv("SUPERADMIN_EMAIL"))),
		Password:    os.Getenv("SUPERADMIN_PASSWORD"),
		DisplayName: strings.TrimSpace(os.Getenv("SUPERADMIN_DISPLAY_NAME")),
	}
	{
		set := 0
		for _, v := range []string{cfg.Superadmin.Email, cfg.Superadmin.Password, cfg.Superadmin.DisplayName} {
			if v != "" {
				set++
			}
		}
		if set != 0 && set != 3 {
			errs = append(errs, "SUPERADMIN_EMAIL, SUPERADMIN_PASSWORD, and SUPERADMIN_DISPLAY_NAME must all be set together (or all unset to skip the startup superadmin bootstrap)")
		}
		if cfg.Superadmin.Password != "" && len(cfg.Superadmin.Password) < 8 {
			errs = append(errs, "SUPERADMIN_PASSWORD must be at least 8 characters")
		}
	}

	if len(cfg.JWT.Secret) < 32 {
		errs = append(errs, fmt.Sprintf(
			"JWT_SECRET must be at least 32 characters (got %d)", len(cfg.JWT.Secret)))
	}

	if len(errs) > 0 {
		return nil, errors.New("config errors: " + strings.Join(errs, "; "))
	}
	return cfg, nil
}

func getenvDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// buildCORSOrigins returns the allowed browser origins for credentialed CORS.
// The SPA origin (FRONTEND_BASE_URL) is always included; CORS_ALLOWED_ORIGINS
// is an optional comma-separated list of additional origins. Wildcard is not
// allowed here because credentialed requests forbid Access-Control-Allow-Origin: *.
func buildCORSOrigins(frontend, extra string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(o string) {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o == "" || seen[o] {
			return
		}
		seen[o] = true
		out = append(out, o)
	}
	add(frontend)
	for _, o := range strings.Split(extra, ",") {
		add(o)
	}
	return out
}

func requireEnv(key string, errs *[]string) string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		*errs = append(*errs, key+" is required")
	}
	return v
}

func parseIntDefault(key string, fallback int, errs *[]string) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, key+" must be an integer, got "+v)
		return fallback
	}
	return n
}

func parseBoolDefault(key string, fallback bool, errs *[]string) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*errs = append(*errs, key+" must be a boolean (true/false/1/0), got "+v)
		return fallback
	}
	return b
}
