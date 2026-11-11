package middleware

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/auth"
)

const claimsContextKey = "auth.claims"

func RequireJWT(signer *auth.JWTSigner) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Request().Header.Get("Authorization")
			if h == "" || !strings.HasPrefix(h, "Bearer ") {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
			}
			tok := strings.TrimPrefix(h, "Bearer ")
			claims, err := signer.Verify(tok)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			}
			c.Set(claimsContextKey, claims)
			return next(c)
		}
	}
}

func MustClaims(c echo.Context) *auth.Claims {
	v := c.Get(claimsContextKey)
	claims, ok := v.(*auth.Claims)
	if !ok {
		panic("auth claims not present; RequireJWT must run before this handler")
	}
	return claims
}
