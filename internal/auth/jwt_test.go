package auth_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
)

func newTestSigner(t *testing.T) *auth.JWTSigner {
	t.Helper()
	return auth.NewJWTSigner("super-long-test-secret-string-32-chars+", "test-issuer", 60)
}

func TestSignVerifyRoundTrip(t *testing.T) {
	signer := newTestSigner(t)
	user := uuid.New()
	tenant := uuid.New()
	levels := []uuid.UUID{uuid.New(), uuid.New()}

	tok, err := signer.Sign(user, tenant, "admin", levels)
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	got, err := signer.Verify(tok)
	require.NoError(t, err)
	require.Equal(t, user, got.UserID)
	require.Equal(t, tenant, got.TenantID)
	require.Equal(t, "admin", got.UserType)
	require.True(t, got.IsAdmin())
	require.False(t, got.IsSuperadmin())
	require.ElementsMatch(t, levels, got.LevelIDs)
}

func TestSignVerifyRoundTrip_Superadmin(t *testing.T) {
	signer := newTestSigner(t)
	tok, err := signer.Sign(uuid.New(), uuid.New(), "superadmin", nil)
	require.NoError(t, err)
	got, err := signer.Verify(tok)
	require.NoError(t, err)
	require.True(t, got.IsAdmin())
	require.True(t, got.IsSuperadmin())
	require.Empty(t, got.LevelIDs)
}

func TestSignVerifyRoundTrip_Worker(t *testing.T) {
	signer := newTestSigner(t)
	tok, err := signer.Sign(uuid.New(), uuid.New(), "worker", []uuid.UUID{uuid.New()})
	require.NoError(t, err)
	got, err := signer.Verify(tok)
	require.NoError(t, err)
	require.False(t, got.IsAdmin())
	require.False(t, got.IsSuperadmin())
}

func TestVerify_RejectsInvalidUserType(t *testing.T) {
	signer := newTestSigner(t)
	tok, err := signer.Sign(uuid.New(), uuid.New(), "not-a-real-type", nil)
	require.NoError(t, err)
	_, err = signer.Verify(tok)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid user_type")
}

func TestVerify_ReturnsErrorWhenTokenSignedWithDifferentSecret(t *testing.T) {
	a := auth.NewJWTSigner("secret-aaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "x", 60)
	b := auth.NewJWTSigner("secret-bbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "x", 60)
	tok, err := a.Sign(uuid.New(), uuid.New(), "worker", nil)
	require.NoError(t, err)
	_, err = b.Verify(tok)
	require.Error(t, err)
}

func TestVerify_ReturnsErrorWhenTokenExpired(t *testing.T) {
	signer := auth.NewJWTSigner("super-long-test-secret-string-32-chars+", "x", 1)
	tok, err := signer.Sign(uuid.New(), uuid.New(), "worker", nil)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
	_, err = signer.Verify(tok)
	require.Error(t, err)
}

func TestSignWithSessionRoundTripsSid(t *testing.T) {
	signer := newTestSigner(t)
	uid, tid, sid := uuid.New(), uuid.New(), uuid.New()

	tok, err := signer.SignWithSession(uid, tid, "worker", nil, sid)
	require.NoError(t, err)

	claims, err := signer.Verify(tok)
	require.NoError(t, err)
	require.Equal(t, sid, claims.SessionID)
	require.Equal(t, uid, claims.UserID)
	require.Equal(t, tid, claims.TenantID)
}

func TestSignWithoutSessionHasNilSid(t *testing.T) {
	signer := newTestSigner(t)
	tok, err := signer.Sign(uuid.New(), uuid.New(), "admin", nil)
	require.NoError(t, err)

	claims, err := signer.Verify(tok)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, claims.SessionID)
}
