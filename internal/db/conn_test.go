package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/config"
	"github.com/Bold322/bold-back-boiler/internal/db"
	"github.com/Bold322/bold-back-boiler/internal/testdb"
)

// requireTestDB skips the test unless RUN_DB_TESTS=1 is set, so unit
// CI without Postgres still passes. Integration tests run against the
// docker-compose Postgres (or TEST_DATABASE_URL).
func requireTestDB(t *testing.T) *config.PostgresConfig {
	t.Helper()
	if os.Getenv("RUN_DB_TESTS") != "1" {
		t.Skip("set RUN_DB_TESTS=1 to run tests that hit Postgres")
	}
	cfg := testdb.PostgresConfig()
	return &cfg
}

func TestNewPool_ReturnsConnectableHandleWhenConfigValid(t *testing.T) {
	cfg := requireTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, *cfg)
	require.NoError(t, err)
	defer pool.Close()

	var one int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&one)
	require.NoError(t, err)
	require.Equal(t, 1, one)
}

func TestNewPool_ReturnsErrorWhenHostUnreachable(t *testing.T) {
	bad := config.PostgresConfig{
		Host: "localhost", Port: 1, Database: "x", User: "x", Password: "x",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := db.NewPool(ctx, bad)
	require.Error(t, err)
}
