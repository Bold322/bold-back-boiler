// cmd/seed creates a tenant plus the initial admin invite. Intended for
// operational onboarding (run once per new customer); not wired into the
// HTTP API. Runs the same bootstrap orchestration as POST /superadmin/tenants
// (superadmin.Service.CreateTenant) but with a no-op email sender: the raw
// invite token is printed so the operator can hand it off out-of-band.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/config"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
	"github.com/Bold322/bold-back-boiler/internal/invites"
	"github.com/Bold322/bold-back-boiler/internal/superadmin"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
)

func main() {
	name := flag.String("name", "", "tenant display name")
	slug := flag.String("slug", "", "tenant slug")
	adminEmail := flag.String("admin-email", "", "initial admin email")
	flag.Parse()

	if *name == "" || *slug == "" || *adminEmail == "" {
		fmt.Fprintln(os.Stderr, "usage: seed -name=... -slug=... -admin-email=...")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.Postgres.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(1)
	}
	defer pool.Close()

	q := sqlc.New(pool)
	tenantSvc := tenants.NewService(pool, q)
	inviteSvc := invites.NewService(pool, q, email.NoopSender{})
	svc := superadmin.NewService(pool, q, tenantSvc, inviteSvc)

	out, err := svc.CreateTenant(ctx, tenants.CreateTenantInput{
		Name: *name, Slug: *slug,
		InitialAdminEmail: *adminEmail,
		InviteTTLHours:    cfg.Invites.TTLHours,
		PublicBaseURL:     cfg.Email.PublicBaseURL,
	})
	if err != nil {
		slog.Error("CreateTenant", "err", err.Error())
		os.Exit(1)
	}
	fmt.Println("tenant id:", out.Tenant.ID)
	fmt.Println("invite token:", out.InviteToken)
	fmt.Println("accept url:", out.AcceptURL)
	fmt.Println("expires at:", out.ExpiresAt.Format(time.RFC3339))
}
