package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

// RefreshSweepLockID is the Postgres advisory-lock key ensuring only one
// replica sweeps dead refresh tokens per tick. Distinct from other sweepers.
const RefreshSweepLockID int64 = 0x52465348_53575052 // ASCII "RFSHSWPR"

type RefreshSweeper struct {
	pool           *pgxpool.Pool
	q              *sqlc.Queries
	retentionHours int
}

func NewRefreshSweeper(pool *pgxpool.Pool, q *sqlc.Queries, retentionHours int) *RefreshSweeper {
	if retentionHours <= 0 {
		retentionHours = 720
	}
	return &RefreshSweeper{pool: pool, q: q, retentionHours: retentionHours}
}

// SweepOnce deletes expired rows and revoked rows older than the retention
// window. Returns the number of rows removed.
func (s *RefreshSweeper) SweepOnce(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteDeadRefreshTokens(ctx, int32(s.retentionHours))
	if err != nil {
		return 0, fmt.Errorf("delete dead refresh tokens: %w", err)
	}
	return n, nil
}

func (s *RefreshSweeper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("refresh sweeper shutting down")
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

func (s *RefreshSweeper) Tick(ctx context.Context) bool {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		slog.Error("refresh sweeper acquire conn", "err", err.Error())
		return false
	}
	defer conn.Release()

	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", RefreshSweepLockID).Scan(&ok); err != nil {
		slog.Error("refresh sweeper try_advisory_lock", "err", err.Error())
		return false
	}
	if !ok {
		return false
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", RefreshSweepLockID); err != nil {
			slog.Error("refresh sweeper advisory_unlock", "err", err.Error())
		}
	}()

	n, err := s.SweepOnce(ctx)
	if err != nil {
		slog.Error("refresh sweep failed", "err", err.Error())
		return true
	}
	if n > 0 {
		slog.Info("refresh sweep completed", "deleted", n)
	}
	return true
}
