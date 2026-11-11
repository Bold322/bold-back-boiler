// cmd/server is the single API binary. Startup order: config -> Postgres
// pool -> embedded migrations -> optional superadmin bootstrap -> services ->
// Echo router -> background sweepers. Everything is wired here so the
// internal packages stay free of construction logic.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Bold322/bold-back-boiler/internal/admin"
	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/config"
	"github.com/Bold322/bold-back-boiler/internal/db"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
	"github.com/Bold322/bold-back-boiler/internal/invites"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
	"github.com/Bold322/bold-back-boiler/internal/server"
	"github.com/Bold322/bold-back-boiler/internal/superadmin"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
	"github.com/Bold322/bold-back-boiler/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.Postgres)
	if err != nil {
		slog.Error("db connect failed", "error", err.Error())
		os.Exit(1)
	}
	defer pool.Close()

	if err := migrations.Up(cfg.Postgres.DSN(), cfg.DefaultTenant.Name, cfg.DefaultTenant.Slug, cfg.DefaultTenant.PlatformSlug); err != nil {
		slog.Error("migrations failed", "error", err.Error())
		os.Exit(1)
	}
	slog.Info("migrations up to date")

	q := sqlc.New(pool)

	// Optional: seed the first platform superadmin from env so a fresh
	// deployment is usable without cmd/superadmin-create. Idempotent: an
	// existing superadmin with this email is left untouched (the env
	// password never overwrites it).
	if cfg.Superadmin.Enabled() {
		if existing, err := q.GetSuperadminByEmail(ctx, cfg.Superadmin.Email); err == nil {
			slog.Info("superadmin bootstrap: already exists, skipping",
				"email", cfg.Superadmin.Email, "id", existing.ID.String())
		} else if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("superadmin bootstrap: lookup failed", "error", err.Error())
			os.Exit(1)
		} else {
			hash, herr := auth.HashPassword(cfg.Superadmin.Password)
			if herr != nil {
				slog.Error("superadmin bootstrap: hash failed", "error", herr.Error())
				os.Exit(1)
			}
			row, cerr := q.CreateSuperadmin(ctx, sqlc.CreateSuperadminParams{
				Email:        cfg.Superadmin.Email,
				PasswordHash: hash,
				DisplayName:  cfg.Superadmin.DisplayName,
			})
			if cerr != nil {
				slog.Error("superadmin bootstrap: create failed", "error", cerr.Error())
				os.Exit(1)
			}
			slog.Info("superadmin bootstrap: created",
				"email", cfg.Superadmin.Email,
				"id", row.ID.String(),
				"tenant_id", row.TenantID.String())
		}
	}

	// Email sender: SES when enabled, otherwise a noop logger so dev needs no AWS creds.
	var sender email.Sender
	if cfg.Email.Delivery == "on" {
		ses, err := email.NewSESSender(ctx, email.SESConfig{
			Region: cfg.Email.SESRegion, FromAddress: cfg.Email.SESFromAddr,
		})
		if err != nil {
			slog.Error("ses init", "err", err.Error())
			os.Exit(1)
		}
		sender = ses
	} else {
		sender = email.NoopSender{}
	}

	signer := auth.NewJWTSigner(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.TTLSeconds)

	sameSite := resolveSameSite(cfg.Refresh.CookieSameSite)
	secure := strings.HasPrefix(strings.ToLower(cfg.FrontendBaseURL), "https")
	if sameSite == http.SameSiteNoneMode {
		secure = true // SameSite=None cookies are rejected by browsers without Secure.
	}
	refreshCfg := auth.RefreshConfig{
		CookieName:   cfg.Refresh.CookieName,
		CookieDomain: cfg.Refresh.CookieDomain,
		SameSite:     sameSite,
		Secure:       secure,
		IdleTTL:      time.Duration(cfg.Refresh.IdleHours) * time.Hour,
		AbsoluteTTL:  time.Duration(cfg.Refresh.AbsoluteHours) * time.Hour,
	}
	authSvc := auth.NewService(pool, q, signer, sender, cfg.FrontendBaseURL, refreshCfg)

	tenantSvc := tenants.NewService(pool, q)
	inviteSvc := invites.NewService(pool, q, sender)
	adminSvc := admin.NewService(pool, q)
	superSvc := superadmin.NewService(pool, q, tenantSvc, inviteSvc)
	superSvc.SetAdmin(adminSvc)
	// Invite accept links point at the SPA's /invite?token= page, so the
	// base URL for invite emails is the frontend origin (FRONTEND_BASE_URL),
	// not the API's PUBLIC_BASE_URL.
	superH := superadmin.NewHandler(
		superSvc,
		cfg.Invites.TTLHours,
		cfg.Tenants.DeleteGraceHours,
		cfg.FrontendBaseURL,
	)
	inviteH := invites.NewHandler(inviteSvc, cfg.Invites.TTLHours, cfg.FrontendBaseURL)
	acceptH := invites.NewAcceptHandler(inviteSvc, authSvc)
	rateLimit := middleware.NewRateLimit(10, time.Minute)

	srv := server.New(cfg, pool, server.Deps{
		Signer:    signer,
		AuthSvc:   authSvc,
		AdminSvc:  adminSvc,
		InviteH:   inviteH,
		AcceptH:   acceptH,
		SuperH:    superH,
		RateLimit: rateLimit,
	})

	if cfg.Tenants.SweepEnabled {
		tsw := tenants.NewSweeper(pool, tenantSvc, tenants.SweepConfig{
			GraceHours: cfg.Tenants.DeleteGraceHours,
			BatchSize:  cfg.Tenants.SweepBatchSize,
		})
		go tsw.Run(ctx, time.Duration(cfg.Tenants.SweepIntervalMinutes)*time.Minute)
		slog.Info("tenant sweeper started",
			"interval_min", cfg.Tenants.SweepIntervalMinutes,
			"grace_hours", cfg.Tenants.DeleteGraceHours)
	}

	if cfg.Refresh.SweepEnabled {
		rsw := auth.NewRefreshSweeper(pool, q, cfg.Refresh.RetentionHours)
		go rsw.Run(ctx, time.Duration(cfg.Refresh.SweepIntervalMinutes)*time.Minute)
		slog.Info("refresh token sweeper started",
			"interval_min", cfg.Refresh.SweepIntervalMinutes,
			"retention_hours", cfg.Refresh.RetentionHours)
	}

	slog.Info("server starting", "host", cfg.API.Host, "port", cfg.API.Port)
	if err := srv.Run(ctx); err != nil {
		slog.Error("server exited", "error", err.Error())
		os.Exit(1)
	}
	slog.Info("server stopped")
}

func resolveSameSite(s string) http.SameSite {
	switch s {
	case "none":
		return http.SameSiteNoneMode
	case "strict":
		return http.SameSiteStrictMode
	default:
		return http.SameSiteLaxMode
	}
}
