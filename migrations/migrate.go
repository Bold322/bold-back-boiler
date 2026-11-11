// Package migrations embeds the SQL migration files into the binary and
// applies them at server startup. Running here (rather than in a separate job)
// is safe across replicas because golang-migrate acquires a Postgres advisory
// lock for the duration of Up().
package migrations

import (
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var migrationFiles embed.FS

// Up applies all pending migrations. The DSN must be the same postgres://...
// string the rest of the app uses; we rewrite the scheme to pgx5:// because
// golang-migrate's pgx/v5 database driver registers under that scheme.
// defaultTenantName, defaultTenantSlug, and platformTenantSlug are injected as
// Postgres startup parameters (app.default_tenant_name, app.default_tenant_slug,
// app.platform_tenant_slug) so migration SQL can read them via current_setting().
// Direct startup params are used rather than the options=-c approach because the
// latter splits on whitespace at the backend argument-parser level, breaking
// values that contain spaces.
func Up(postgresDSN, defaultTenantName, defaultTenantSlug, platformTenantSlug string) error {
	src, err := iofs.New(migrationFiles, ".")
	if err != nil {
		return fmt.Errorf("init migration source: %w", err)
	}

	dsn := strings.Replace(postgresDSN, "postgres://", "pgx5://", 1)

	parsed, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	// Pass GUCs as direct startup parameters so the wire protocol delivers
	// them verbatim to Postgres. The options=-c approach splits on whitespace
	// at the backend argument-parser level, which breaks values that contain
	// spaces (e.g. "Test Default"). Startup parameters bypass that split.
	q := parsed.Query()
	q.Set("app.default_tenant_name", defaultTenantName)
	q.Set("app.default_tenant_slug", defaultTenantSlug)
	q.Set("app.platform_tenant_slug", platformTenantSlug)
	parsed.RawQuery = q.Encode()
	dsn = parsed.String()

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
