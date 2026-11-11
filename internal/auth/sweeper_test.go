package auth_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

// insertRefreshRow inserts a refresh_tokens row directly via sqlc with
// expires_at = now()+offset (a negative offset yields an already-expired
// row). Returns the sha256 hash of the raw token, the family id, and the raw
// token itself, so callers can look the row up via GetRefreshTokenByHash the
// same way production code does.
func insertRefreshRow(t *testing.T, ctx context.Context, q *sqlc.Queries, tenantID, userID uuid.UUID, offset time.Duration) (hash []byte, familyID uuid.UUID, raw string) {
	t.Helper()
	raw = uuid.New().String()
	sum := sha256.Sum256([]byte(raw))
	familyID = uuid.New()
	now := time.Now()
	err := q.InsertRefreshToken(ctx, sqlc.InsertRefreshTokenParams{
		TenantID:        tenantID,
		UserID:          userID,
		FamilyID:        familyID,
		TokenHash:       sum[:],
		ExpiresAt:       pgtype.Timestamptz{Time: now.Add(offset), Valid: true},
		FamilyStartedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	require.NoError(t, err)
	return sum[:], familyID, raw
}

func TestRefreshSweeperDeletesExpired(t *testing.T) {
	skipIfNoDB(t)
	ctx := context.Background()
	pool := newTestPool(t)
	tenantID, userID, _ := seedUser(t, pool)
	q := sqlc.New(pool)

	// expired row
	past, _, _ := insertRefreshRow(t, ctx, q, tenantID, userID, -time.Hour)
	// live row
	live, _, _ := insertRefreshRow(t, ctx, q, tenantID, userID, time.Hour)

	sw := auth.NewRefreshSweeper(pool, q, 720)
	n, err := sw.SweepOnce(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1))

	_, err = q.GetRefreshTokenByHash(ctx, past)
	require.Error(t, err) // gone
	_, err = q.GetRefreshTokenByHash(ctx, live)
	require.NoError(t, err) // survives
}
