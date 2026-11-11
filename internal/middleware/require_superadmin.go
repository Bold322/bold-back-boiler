package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// RequireSuperadmin rejects callers whose JWT does not carry
// user_type=superadmin. Must be chained after RequireJWT. Does NOT
// chain RequireActiveTenant: superadmins live in the synthetic
// platform tenant which is always active and undeletable.
func RequireSuperadmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := MustClaims(c)
			if !claims.IsSuperadmin() {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "superadmin required"})
			}
			return next(c)
		}
	}
}
