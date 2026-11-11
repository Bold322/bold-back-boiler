package invites_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
	"github.com/Bold322/bold-back-boiler/internal/invites"
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

// seedTenant inserts a fresh tenant with one department and one worker
// level, plus an admin user. Returns (tenantID, workerLevelID, inviterUserID).
func seedTenant(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	var tid, deptID, workerLevel, inviter uuid.UUID
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('Inv', 'inv-' || substr(md5(random()::text), 1, 8)) RETURNING id`,
	).Scan(&tid); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	// Registered right after the tenant exists so a failure in any of the
	// following inserts still cleans up. A plain "DELETE FROM tenants" cascade
	// is not enough once an invite has been accepted: invites.invited_by and
	// user_department_memberships.granted_by both reference users(id) with no
	// ON DELETE action, and each can point at a user this same statement is
	// also cascade-deleting via a different path (tenants -> users), which
	// Postgres does not order safely against a NO ACTION/RESTRICT sibling. So
	// both are cleared explicitly, in their own statements, before the tenant
	// delete.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_department_memberships
			WHERE user_id IN (SELECT id FROM users WHERE tenant_id = $1)
			   OR granted_by IN (SELECT id FROM users WHERE tenant_id = $1)`, tid)
		_, _ = pool.Exec(ctx, `DELETE FROM invites WHERE tenant_id = $1`, tid)
		_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
	})
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (tenant_id, name, description) VALUES ($1, 'General', '') RETURNING id`,
		tid,
	).Scan(&deptID); err != nil {
		t.Fatalf("dept: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO department_levels (department_id, name, rank) VALUES ($1, 'Worker', 1) RETURNING id`,
		deptID,
	).Scan(&workerLevel); err != nil {
		t.Fatalf("level: %v", err)
	}
	adminEmail := "admin-" + strings.ToLower(uuid.New().String()[:8]) + "@x.com"
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash, display_name, user_type)
         VALUES ($1, $2, '', 'Admin', 'admin') RETURNING id`, tid, adminEmail,
	).Scan(&inviter); err != nil {
		t.Fatalf("user: %v", err)
	}
	return tid, workerLevel, inviter
}

func TestIssue_Success(t *testing.T) {
	pool := newPool(t)
	svc := invites.NewService(pool, sqlc.New(pool), email.NoopSender{})

	tid, workerLevelID, inviter := seedTenant(t, pool)

	out, err := svc.Issue(context.Background(), invites.IssueInput{
		TenantID: tid, Email: "bob@example.com",
		UserType: "worker",
		LevelIDs: []uuid.UUID{workerLevelID}, InviterID: inviter,
		TTLHours: 168, BaseURL: "https://app.example.com",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if out.Token == "" {
		t.Errorf("token empty")
	}
	if out.Invite.Email != "bob@example.com" {
		t.Errorf("email mismatch")
	}
}

func TestAccept_Success(t *testing.T) {
	pool := newPool(t)
	svc := invites.NewService(pool, sqlc.New(pool), email.NoopSender{})

	tid, workerLevelID, inviter := seedTenant(t, pool)
	out, err := svc.Issue(context.Background(), invites.IssueInput{
		TenantID: tid, Email: "bob@example.com",
		UserType: "worker",
		LevelIDs: []uuid.UUID{workerLevelID}, InviterID: inviter,
		TTLHours: 168, BaseURL: "https://app",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	acc, err := svc.Accept(context.Background(), invites.AcceptInput{
		Token: out.Token, Password: "correct horse battery staple", DisplayName: "Bob",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if acc.TenantID != tid {
		t.Errorf("tenant mismatch")
	}
	if len(acc.LevelIDs) != 1 || acc.LevelIDs[0] != workerLevelID {
		t.Errorf("level ids mismatch")
	}
	if acc.UserType != "worker" {
		t.Errorf("user_type = %q, want worker", acc.UserType)
	}
}

func TestAccept_WrongToken(t *testing.T) {
	pool := newPool(t)
	svc := invites.NewService(pool, sqlc.New(pool), email.NoopSender{})
	_, err := svc.Accept(context.Background(), invites.AcceptInput{
		Token: "wrong", Password: "x", DisplayName: "x",
	})
	if err == nil {
		t.Fatalf("expected error for wrong token")
	}
}
