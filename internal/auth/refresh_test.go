package auth_test

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
	"github.com/Bold322/bold-back-boiler/internal/testdb"
)

// testDSN is the integration-test database. Override with TEST_DATABASE_URL;
// the default matches docker-compose.yml.
var testDSN = testdb.DSN()

func skipIfNoDB(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") == "" {
		t.Skip("set RUN_DB_TESTS=1 to run DB integration tests")
	}
}

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// seedUser inserts a fresh tenant with one department and one level plus a
// single worker user, returning (tenantID, userID, levelID). The user starts
// with NO membership on levelID (tests grant it explicitly). Deleting the
// tenant on cleanup cascades to the user and any refresh tokens (both FKs are
// ON DELETE CASCADE).
func seedUser(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var tid, deptID, levelID, userID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('Auth', 'auth-' || substr(md5(random()::text), 1, 8)) RETURNING id`,
	).Scan(&tid); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (tenant_id, name, description) VALUES ($1, 'General', '') RETURNING id`, tid,
	).Scan(&deptID); err != nil {
		t.Fatalf("dept: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO department_levels (department_id, name, rank) VALUES ($1, 'Employee', 1) RETURNING id`, deptID,
	).Scan(&levelID); err != nil {
		t.Fatalf("level: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
         VALUES ($1, 'worker-' || substr(md5(random()::text), 1, 8) || '@x.com', '', 'Worker', 'worker') RETURNING id`, tid,
	).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tid)
	})
	return tid, userID, levelID
}

// newTestAuthServiceFull builds an auth.Service over a freshly seeded tenant and
// returns the pool + ids so security-branch tests can mutate rows directly.
func newTestAuthServiceFull(t *testing.T, cfg auth.RefreshConfig) (svc *auth.Service, pool *pgxpool.Pool, userID, tenantID, levelID uuid.UUID) {
	t.Helper()
	pool = newTestPool(t)
	tenantID, userID, levelID = seedUser(t, pool)
	signer := auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900)
	svc = auth.NewService(pool, sqlc.New(pool), signer, email.NoopSender{}, "http://localhost:4200", cfg)
	return svc, pool, userID, tenantID, levelID
}

func newTestAuthService(t *testing.T, cfg auth.RefreshConfig) (*auth.Service, uuid.UUID, uuid.UUID) {
	svc, _, userID, tenantID, _ := newTestAuthServiceFull(t, cfg)
	return svc, userID, tenantID
}

// familyIDFor resolves the refresh-token family that a minted raw token belongs
// to, by hashing the token the same way the service does and looking up the row.
func familyIDFor(t *testing.T, pool *pgxpool.Pool, rawToken string) uuid.UUID {
	t.Helper()
	h := sha256.Sum256([]byte(rawToken))
	var fid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT family_id FROM refresh_tokens WHERE token_hash = $1`, h[:],
	).Scan(&fid); err != nil {
		t.Fatalf("lookup family_id: %v", err)
	}
	return fid
}

// countLiveFamily returns how many rows of a family are neither rotated nor
// revoked, i.e. still usable. Zero proves the family was killed.
func countLiveFamily(t *testing.T, pool *pgxpool.Pool, familyID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens WHERE family_id = $1 AND revoked_at IS NULL`, familyID,
	).Scan(&n); err != nil {
		t.Fatalf("count live family: %v", err)
	}
	return n
}

func TestRefreshRotationAndReuse(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()

	svc, userID, tenantID := newTestAuthService(t, auth.RefreshConfig{
		CookieName: "app_refresh", SameSite: 0, Secure: false,
		IdleTTL: 14 * 24 * time.Hour, AbsoluteTTL: 90 * 24 * time.Hour,
	})

	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)
	require.NotEmpty(t, minted.RefreshToken)
	require.NotEmpty(t, minted.AccessToken)

	// Rotating once yields a new token pair.
	rotated, err := svc.Refresh(ctx, minted.RefreshToken)
	require.NoError(t, err)
	require.NotEqual(t, minted.RefreshToken, rotated.RefreshToken)
	require.NotEmpty(t, rotated.AccessToken)

	// Reusing the now-rotated original is detected and kills the family.
	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshReuse)

	// After family revocation, the previously-valid rotated token is dead too.
	// RevokeRefreshFamily set revoked_at on every family member (including this
	// rotated token), so presenting it hits the revoked branch and reports
	// reuse rather than plain-invalid. Either way the token no longer refreshes.
	_, err = svc.Refresh(ctx, rotated.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshReuse)
}

func TestLogoutRevokesFamily(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, userID, tenantID := newTestAuthService(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})
	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)
	require.NoError(t, svc.Logout(ctx, minted.RefreshToken))
	// A revoked token presented to Refresh is treated as reuse.
	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshReuse)
}

func TestRefreshInvalidToken(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, _, _ := newTestAuthService(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})
	_, err := svc.Refresh(ctx, "")
	require.ErrorIs(t, err, auth.ErrRefreshMissing)
	_, err = svc.Refresh(ctx, "not-a-real-token")
	require.ErrorIs(t, err, auth.ErrRefreshInvalid)
}

func TestLogoutUnknownTokenIsNoop(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, _, _ := newTestAuthService(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})
	require.NoError(t, svc.Logout(ctx, ""))
	require.NoError(t, svc.Logout(ctx, "unknown-token"))
}

// TestRefreshAbsoluteCapRejects covers the absolute-lifetime branch: a token
// still inside its idle window but past the family's absolute cap is rejected
// and the family is revoked. family_started_at is pushed 200 days back against
// the default 90-day AbsoluteTTL; the idle expires_at (now + 14d) stays valid,
// so only the absolute cap can trip.
func TestRefreshAbsoluteCapRejects(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, pool, userID, tenantID, _ := newTestAuthServiceFull(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 14 * 24 * time.Hour, AbsoluteTTL: 90 * 24 * time.Hour,
	})

	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)
	familyID := familyIDFor(t, pool, minted.RefreshToken)

	_, err = pool.Exec(ctx,
		`UPDATE refresh_tokens SET family_started_at = now() - interval '200 days' WHERE family_id = $1`, familyID)
	require.NoError(t, err)

	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshInvalid)
	require.Equal(t, 0, countLiveFamily(t, pool, familyID), "family must be revoked after absolute-cap rejection")
}

// TestRefreshRejectsSoftDeletedUser covers the deleted-user branch: refreshing
// after the owning user is soft-deleted is rejected and the family is revoked.
func TestRefreshRejectsSoftDeletedUser(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, pool, userID, tenantID, _ := newTestAuthServiceFull(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})

	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `UPDATE users SET deleted_at = now() WHERE id = $1`, userID)
	require.NoError(t, err)

	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshInvalid)
	// The family was revoked, so re-presenting the same token now hits the
	// revoked branch and reports reuse.
	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshReuse)
}

// TestRefreshRejectsSuspendedTenant covers the suspended-tenant branch.
func TestRefreshRejectsSuspendedTenant(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, pool, userID, tenantID, _ := newTestAuthServiceFull(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})

	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `UPDATE tenants SET status = 'suspended' WHERE id = $1`, tenantID)
	require.NoError(t, err)

	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.ErrorIs(t, err, auth.ErrRefreshInvalid)
}

// TestRefreshReloadsFreshClaims proves Refresh re-reads level ids from the DB
// rather than reusing stale claims: a level granted AFTER mint shows up in the
// rotated access token, and the sid claim stays pinned to the original family.
func TestRefreshReloadsFreshClaims(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, pool, userID, tenantID, levelID := newTestAuthServiceFull(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})

	// Mint with no levels (the user holds no membership yet).
	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)
	familyID := familyIDFor(t, pool, minted.RefreshToken)

	// Grant a new department level to the user, then rotate.
	_, err = pool.Exec(ctx,
		`INSERT INTO user_department_memberships (user_id, department_level_id, granted_by) VALUES ($1, $2, $1)`,
		userID, levelID)
	require.NoError(t, err)

	rotated, err := svc.Refresh(ctx, minted.RefreshToken)
	require.NoError(t, err)

	verifier := auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900)
	claims, err := verifier.Verify(rotated.AccessToken)
	require.NoError(t, err)
	require.Contains(t, claims.LevelIDs, levelID, "refreshed access token must carry the newly granted level")
	require.Equal(t, familyID, claims.SessionID, "sid must stay pinned to the original family")
}
