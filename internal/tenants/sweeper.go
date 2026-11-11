package tenants

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

// SweepLockID is the Postgres advisory lock id used by the tenant sweeper.
// Distinct from documents.SweepLockID so a hung document sweep does not block
// tenant cleanup.
const SweepLockID int64 = 0x54454E54_53575052 // ASCII "TENTSWPR"

type Sweeper struct {
	pool *pgxpool.Pool
	svc  *Service
	cfg  SweepConfig
}

type SweepConfig struct {
	GraceHours int
	BatchSize  int
}

func NewSweeper(pool *pgxpool.Pool, svc *Service, cfg SweepConfig) *Sweeper {
	return &Sweeper{pool: pool, svc: svc, cfg: cfg}
}

func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tick(ctx); err != nil {
				slog.Error("tenant sweeper tick", "err", err.Error())
			}
		}
	}
}

func (s *Sweeper) tick(ctx context.Context) error {
	var acquired bool
	if err := s.pool.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, SweepLockID).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() {
		_, _ = s.pool.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, SweepLockID)
	}()

	q := sqlc.New(s.pool)
	rows, err := q.HardDeleteExpiredTenants(ctx, int32(s.cfg.GraceHours))
	if err != nil {
		return err
	}
	for _, id := range rows {
		// FK cascades already happened inside HardDeleteExpiredTenants. Add
		// external cleanup (object storage, third-party accounts) here.
		slog.Info("tenant hard-deleted", "tenant", id.String())
	}
	return nil
}
