package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// RequireAdmin rejects callers whose JWT does not carry admin or
// superadmin user_type. Superadmins inherit admin management routes.
// Must be chained after RequireJWT.
func RequireAdmin() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := MustClaims(c)
			if !claims.IsAdmin() {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "admin required"})
			}
			return next(c)
		}
	}
}
