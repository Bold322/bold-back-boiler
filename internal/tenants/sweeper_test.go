package tenants_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
)

func TestSweeper_HardDeletesAfterGrace(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1")
	}
	pool, err := pgxpool.New(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var tid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO tenants (name, slug, deleted_at, status)
         VALUES ('to-sweep', 'sw-' || substr(md5(random()::text), 1, 8), now() - interval '200 hours', 'suspended')
         RETURNING id`).Scan(&tid); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tid)
	})

	svc := tenants.NewService(pool, sqlc.New(pool))
	sw := tenants.NewSweeper(pool, svc, tenants.SweepConfig{GraceHours: 168, BatchSize: 10})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go sw.Run(ctx, 100*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	cancel()

	var count int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM tenants WHERE id = $1`, tid).Scan(&count)
	if count != 0 {
		t.Errorf("expected tenant deleted, still present")
	}
}
