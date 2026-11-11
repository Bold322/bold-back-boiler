package auth_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
)

// TestResetPasswordRevokesAllSessions proves a password reset kills every
// live refresh-token session for the user, not just whichever one happened
// to be presented (there is none here: reset-password is unauthenticated).
// A stolen session must not survive its owner resetting their password.
func TestResetPasswordRevokesAllSessions(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	svc, pool, userID, tenantID, _ := newTestAuthServiceFull(t, auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})

	minted, err := svc.MintSession(ctx, userID, tenantID, "worker", nil)
	require.NoError(t, err)

	raw := "reset-token-for-" + userID.String()
	hash := sha256.Sum256([]byte(raw))
	_, err = pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '30 minutes')`,
		userID, hash[:],
	)
	require.NoError(t, err)

	require.NoError(t, svc.ResetPassword(ctx, raw, "brand-new-password-123"))

	_, err = svc.Refresh(ctx, minted.RefreshToken)
	require.Error(t, err, "session minted before the reset must not survive it")
}

// TestUpdateMePasswordRevokesOtherSessionsKeepsCurrent proves the directional
// guarantee of UpdateMe's revoke-others call: changing the password kills
// every OTHER live session (family B) while leaving the session that made the
// request (family A, identified by its own access token's sid claim) alive.
// A transposed FamilyID/UserID argument in the RevokeRefreshTokensForUserExceptFamily
// call would fail the family-A assertion below (it would get revoked too).
func TestUpdateMePasswordRevokesOtherSessionsKeepsCurrent(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	pool := newTestPool(t)

	hash, err := auth.HashPassword("known-pass")
	require.NoError(t, err)

	var tid, userID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('Auth UpdateMe', 'auth-updateme-' || substr(md5(random()::text), 1, 8)) RETURNING id`,
	).Scan(&tid))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
         VALUES ($1, $2, $3, 'Worker', 'worker') RETURNING id`,
		tid, "updateme-"+strings.ToLower(uuid.New().String()[:8])+"@x.com", hash,
	).Scan(&userID))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tid)
	})

	signer := auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900)
	svc := auth.NewService(pool, sqlc.New(pool), signer, email.NoopSender{}, "http://localhost:4200", auth.RefreshConfig{
		CookieName: "app_refresh", IdleTTL: 24 * time.Hour, AbsoluteTTL: 48 * time.Hour,
	})

	mintedA, err := svc.MintSession(ctx, userID, tid, "worker", nil)
	require.NoError(t, err)
	mintedB, err := svc.MintSession(ctx, userID, tid, "worker", nil)
	require.NoError(t, err)

	// sid == family_id: derive family A from the access token itself, exactly
	// as the /me route derives it from claims.SessionID.
	claimsA, err := signer.Verify(mintedA.AccessToken)
	require.NoError(t, err)
	familyA := claimsA.SessionID
	require.NotEqual(t, uuid.Nil, familyA)

	_, err = svc.UpdateMe(ctx, userID, familyA, auth.UpdateMeInput{
		NewPassword:     "new-pass-1234",
		CurrentPassword: "known-pass",
	})
	require.NoError(t, err)

	// Family B (not the acting session) must be revoked.
	_, err = svc.Refresh(ctx, mintedB.RefreshToken)
	require.Error(t, err, "the other session must be revoked by the password change")

	// Family A (the acting session) must still work.
	_, err = svc.Refresh(ctx, mintedA.RefreshToken)
	require.NoError(t, err, "the acting session must survive its own password change")
}
