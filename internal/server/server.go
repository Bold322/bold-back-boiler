package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"github.com/Bold322/bold-back-boiler/internal/admin"
	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/config"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/invites"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
	"github.com/Bold322/bold-back-boiler/internal/preferences"
	"github.com/Bold322/bold-back-boiler/internal/superadmin"
)

type Server struct {
	echo *echo.Echo
	cfg  *config.Config
}

// Deps is everything the router needs. main.go constructs the services and
// passes them in; keeping New free of construction logic makes it easy to
// wire a test server with fakes.
type Deps struct {
	Signer    *auth.JWTSigner
	AuthSvc   *auth.Service
	AdminSvc  *admin.Service
	InviteH   *invites.Handler
	AcceptH   *invites.AcceptHandler
	SuperH    *superadmin.Handler
	RateLimit *middleware.RateLimit
}

// New builds the Echo router and registers every route. This file is the
// canonical route map: add your domain handlers to the matching group below
// (public / authed / admin / superadmin).
func New(cfg *config.Config, pool *pgxpool.Pool, d Deps) *Server {
	q := sqlc.New(pool)

	authH := auth.NewHandler(d.AuthSvc)
	signer, inviteH, acceptH, rateLimit, superH := d.Signer, d.InviteH, d.AcceptH, d.RateLimit, d.SuperH

	adminSvc := d.AdminSvc
	if adminSvc == nil {
		adminSvc = admin.NewService(pool, q)
	}
	adminH := admin.NewHandler(adminSvc)
	prefsH := preferences.NewHandler(q)

	e := echo.New()
	e.HideBanner = true
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(echomw.RequestLoggerWithConfig(echomw.RequestLoggerConfig{
		LogMethod:   true,
		LogURI:      true,
		LogStatus:   true,
		LogLatency:  true,
		LogError:    true,
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v echomw.RequestLoggerValues) error {
			attrs := []any{
				"method", v.Method,
				"uri", v.URI,
				"status", v.Status,
				"latency_ms", v.Latency.Milliseconds(),
			}
			if v.Error != nil {
				attrs = append(attrs, "err", v.Error.Error())
				slog.Error("request", attrs...)
			} else {
				slog.Info("request", attrs...)
			}
			return nil
		},
	}))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins:     cfg.CORSAllowedOrigins,
		AllowCredentials: true,
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
			"X-Refresh-Request",
		},
	}))
	e.Use(echomw.BodyLimit("10M"))

	e.GET("/health", func(c echo.Context) error {
		if err := pool.Ping(c.Request().Context()); err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"db": "down"})
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Public routes. Login shares the per-IP limiter with the other public
	// auth endpoints: without it, password login is open to unthrottled
	// credential stuffing (there is no lockout either).
	e.POST("/auth/login", rateLimit.Middleware()(authH.Login))
	e.POST("/auth/accept-invite", rateLimit.Middleware()(acceptH.Accept))
	e.POST("/auth/forgot-password", rateLimit.Middleware()(authH.ForgotPassword))
	e.POST("/auth/reset-password", rateLimit.Middleware()(authH.ResetPassword))
	e.POST("/auth/refresh", rateLimit.Middleware()(authH.Refresh))
	e.POST("/auth/logout", rateLimit.Middleware()(authH.Logout))

	// Google sign-in (server-side auth-code flow). Registered only when
	// configured; absent config -> password-only product, no dead routes.
	if cfg.Google.Enabled() {
		gh := auth.NewGoogleHandler(auth.GoogleConfig{
			ClientID:        cfg.Google.ClientID,
			ClientSecret:    cfg.Google.ClientSecret,
			RedirectURL:     cfg.Google.RedirectURL,
			FrontendBaseURL: cfg.FrontendBaseURL,
		}, d.AuthSvc, []byte(cfg.JWT.Secret))
		e.GET("/auth/google/login", gh.Login)
		e.GET("/auth/google/callback", gh.Callback)
		slog.Info("google oauth enabled", "redirect_url", cfg.Google.RedirectURL)
	}

	// Authed group
	authed := e.Group("",
		middleware.RequireJWT(signer),
		middleware.RequireActiveTenant(pool),
	)
	// Domain routes go here, e.g.:
	//   authed.GET("/projects", projectH.List)
	//   authed.POST("/projects", projectH.Create)
	authed.GET("/me", func(c echo.Context) error {
		return authH.Me(c, middleware.MustClaims(c).UserID)
	})
	authed.PATCH("/me", func(c echo.Context) error {
		cl := middleware.MustClaims(c)
		return authH.UpdateMe(c, cl.UserID, cl.SessionID)
	})
	authed.GET("/departments", adminH.ListDepartmentsPublic)
	authed.GET("/preferences", prefsH.Get)
	authed.PATCH("/preferences", prefsH.Patch)

	// Admin group
	adminGroup := e.Group("/admin",
		middleware.RequireJWT(signer),
		middleware.RequireActiveTenant(pool),
		middleware.RequireAdmin(),
	)
	adminGroup.GET("/users", adminH.List)
	adminGroup.POST("/users/:id/levels/:level_id", adminH.GrantMembership)
	adminGroup.DELETE("/users/:id/levels/:level_id", adminH.RevokeMembership)
	adminGroup.PATCH("/users/:id/user-type", adminH.SetUserType)
	adminGroup.GET("/audit/membership-changes", adminH.ListAudit)
	adminGroup.GET("/stats", adminH.Stats)
	adminGroup.GET("/departments", adminH.ListDepartments)
	adminGroup.POST("/departments", adminH.CreateDepartment)
	adminGroup.PATCH("/departments/:id", adminH.PatchDepartment)
	adminGroup.DELETE("/departments/:id", adminH.DeleteDepartment)
	adminGroup.GET("/departments/:id/levels", adminH.ListLevels)
	adminGroup.POST("/departments/:id/levels", adminH.CreateLevel)
	adminGroup.PATCH("/departments/:id/levels/:level_id", adminH.PatchLevel)
	adminGroup.DELETE("/departments/:id/levels/:level_id", adminH.DeleteLevel)
	adminGroup.DELETE("/users/:id", adminH.DeleteUser)
	adminGroup.POST("/users/:id/restore", adminH.RestoreUser)
	adminGroup.POST("/invites", inviteH.Issue)
	adminGroup.POST("/invites/bulk", inviteH.IssueBulk)
	adminGroup.GET("/invites", inviteH.List)
	adminGroup.DELETE("/invites/:id", inviteH.Revoke)
	adminGroup.POST("/invites/:id/resend", inviteH.Resend)

	// Superadmin group (RequireJWT but NOT RequireActiveTenant; the platform
	// tenant is always active).
	superGroup := e.Group("/superadmin",
		middleware.RequireJWT(signer),
		middleware.RequireSuperadmin(),
	)
	superGroup.GET("/tenants", superH.ListTenants)
	superGroup.POST("/tenants", superH.CreateTenant)
	superGroup.POST("/tenants/:id/suspend", superH.SuspendTenant)
	superGroup.POST("/tenants/:id/unsuspend", superH.UnsuspendTenant)
	superGroup.DELETE("/tenants/:id", superH.DeleteTenant)
	superGroup.POST("/tenants/:id/restore", superH.RestoreTenant)
	superGroup.GET("/announcements", superH.ListAnnouncements)
	superGroup.POST("/announcements", superH.CreateAnnouncement)
	superGroup.DELETE("/announcements/:id", superH.DeleteAnnouncement)
	superGroup.GET("/plans", superH.ListPlans)
	superGroup.PATCH("/plans/:id", superH.UpdatePlan)
	superGroup.GET("/audit", superH.ListSuperAudit)
	superGroup.GET("/stats", superH.GetPlatformStats)
	superGroup.GET("/system/health", superH.GetSystemHealth)
	superGroup.GET("/internal-users", superH.ListInternalUsers)
	superGroup.PATCH("/tenants/:id", superH.PatchTenant)
	superGroup.GET("/tenants/:id/users", superH.ListUsersInTenant)
	superGroup.POST("/tenants/:id/invites", superH.InviteUserInTenant)
	superGroup.DELETE("/tenants/:id/users/:uid", superH.RemoveUserFromTenant)
	superGroup.POST("/tenants/:id/users/:uid/restore", superH.RestoreUserInTenant)

	return &Server{echo: e, cfg: cfg}
}

// shutdownGrace is the time the server gives in-flight requests to finish
// after a SIGTERM/SIGINT. Long-running streams will be cut off at the
// timeout; ordinary requests drain well before then.
const shutdownGrace = 15 * time.Second

// Run starts the HTTP server and blocks until either the listener fails or
// ctx is cancelled. On cancel it calls Echo's graceful Shutdown so in-flight
// requests drain inside shutdownGrace.
func (s *Server) Run(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.API.Host, s.cfg.API.Port)

	errCh := make(chan error, 1)
	go func() {
		if err := s.echo.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("server shutdown initiated")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := s.echo.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
