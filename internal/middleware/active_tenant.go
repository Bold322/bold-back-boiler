package middleware

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

// RequireActiveTenant loads the tenant for the request and rejects any
// request whose tenant is suspended or has deleted_at set. Must be chained
// after RequireJWT so that the request's tenant id is available in claims.
func RequireActiveTenant(pool *pgxpool.Pool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := MustClaims(c)

			var status string
			var deletedAt *time.Time
			err := pool.QueryRow(c.Request().Context(),
				`SELECT status, deleted_at FROM tenants WHERE id = $1`,
				claims.TenantID,
			).Scan(&status, &deletedAt)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return c.JSON(http.StatusGone, map[string]string{"error": "tenant pending deletion"})
				}
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "tenant lookup failed"})
			}
			if deletedAt != nil {
				return c.JSON(http.StatusGone, map[string]string{"error": "tenant pending deletion"})
			}
			if status == "suspended" {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "tenant suspended"})
			}
			return next(c)
		}
	}
}
