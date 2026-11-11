// cmd/superadmin-create creates the first (or an additional) superadmin
// user in the platform tenant. Symmetric to cmd/seed which creates
// tenants. The password is read from stdin (so it does not show up in
// shell history) unless -password is passed explicitly.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/config"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

func main() {
	email := flag.String("email", "", "superadmin email")
	displayName := flag.String("display-name", "", "superadmin display name")
	password := flag.String("password", "", "password (leave empty to read from stdin)")
	flag.Parse()

	if *email == "" || *displayName == "" {
		fmt.Fprintln(os.Stderr, "usage: superadmin-create -email=... -display-name=... [-password=...]")
		os.Exit(2)
	}

	if *password == "" {
		fmt.Fprint(os.Stderr, "password: ")
		r := bufio.NewReader(os.Stdin)
		line, err := r.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "read password:", err)
			os.Exit(1)
		}
		*password = strings.TrimSpace(line)
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

	existing, err := q.GetSuperadminByEmail(ctx, *email)
	if err == nil {
		fmt.Fprintf(os.Stderr, "superadmin with email %q already exists (id=%s)\n", *email, existing.ID)
		os.Exit(1)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		fmt.Fprintln(os.Stderr, "lookup:", err)
		os.Exit(1)
	}

	hash, err := auth.HashPassword(*password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		os.Exit(1)
	}

	row, err := q.CreateSuperadmin(ctx, sqlc.CreateSuperadminParams{
		Email: *email, PasswordHash: hash, DisplayName: *displayName,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
	fmt.Println("superadmin id:", row.ID)
	fmt.Println("platform tenant id:", row.TenantID)
}
