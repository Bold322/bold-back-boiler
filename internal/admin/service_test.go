package admin_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/admin"
	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
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
	p, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// seedTenantWithAdminAndWorker inserts a fresh tenant plus an admin user (the
// caller) and a separate worker user (the delete target). Cleanup mirrors
// internal/invites/service_test.go's seedTenant: memberships and invites are
// cleared explicitly before the tenant delete, since granted_by/invited_by
// reference users(id) with no ON DELETE action and a bare tenant delete can
// race Postgres's cascade ordering across the tenants -> users path.
// refresh_tokens cascade fine off both tenant_id and user_id FKs, so no
// explicit cleanup is needed there.
func seedTenantWithAdminAndWorker(t *testing.T, pool *pgxpool.Pool) (tenantID, adminID, workerID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('Admin', 'admin-' || substr(md5(random()::text), 1, 8)) RETURNING id`,
	).Scan(&tenantID))
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_department_memberships
			WHERE user_id IN (SELECT id FROM users WHERE tenant_id = $1)
			   OR granted_by IN (SELECT id FROM users WHERE tenant_id = $1)`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM invites WHERE tenant_id = $1`, tenantID)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	adminEmail := "admin-" + strings.ToLower(uuid.New().String()[:8]) + "@x.com"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
         VALUES ($1, $2, '', 'Admin', 'admin') RETURNING id`, tenantID, adminEmail,
	).Scan(&adminID))
	workerEmail := "worker-" + strings.ToLower(uuid.New().String()[:8]) + "@x.com"
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
         VALUES ($1, $2, '', 'Worker', 'worker') RETURNING id`, tenantID, workerEmail,
	).Scan(&workerID))
	return tenantID, adminID, workerID
}

// TestDeleteUserRevokesTargetSessions proves the soft-delete revocation
// trigger: an admin deleting another user's account kills every live
// refresh-token session belonging to the deleted target.
func TestDeleteUserRevokesTargetSessions(t *testing.T) {
	pool := newPool(t)
	ctx := context.Background()

	tenantID, callerAdminID, targetID := seedTenantWithAdminAndWorker(t, pool)

	authSvc := auth.NewService(pool, sqlc.New(pool),
		auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900),
		email.NoopSender{}, "http://localhost:4200", auth.RefreshConfig{
			CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
		})

	minted, err := authSvc.MintSession(ctx, targetID, tenantID, "worker", nil)
	require.NoError(t, err)

	adminSvc := admin.NewService(pool, sqlc.New(pool))
	require.NoError(t, adminSvc.DeleteUser(ctx, tenantID, callerAdminID, targetID))

	_, err = authSvc.Refresh(ctx, minted.RefreshToken)
	require.Error(t, err, "the deleted user's session must not survive the soft-delete")
}
