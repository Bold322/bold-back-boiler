package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// RateLimit is a fixed-window rate limiter keyed per caller. Not durable;
// resets when the process restarts. Middleware keys by IP (public routes like
// /auth/accept-invite, where a hard cap defeats brute-force token replay);
// ByUser keys by the authenticated user id (cost-bearing routes such as an
// LLM call or an export). Use a separate instance per route family so budgets
// do not mix.
type RateLimit struct {
	mu      sync.Mutex
	counts  map[string]int
	resetAt map[string]time.Time
	budget  int
	window  time.Duration
}

func NewRateLimit(budget int, window time.Duration) *RateLimit {
	return &RateLimit{
		counts:  make(map[string]int),
		resetAt: make(map[string]time.Time),
		budget:  budget,
		window:  window,
	}
}

func (r *RateLimit) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !r.allow(c.RealIP()) {
				return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
			}
			return next(c)
		}
	}
}

// ByUser is the Middleware variant keyed by the authenticated user id, so a
// shared office IP never starves coworkers and a single user cannot dodge the
// budget by rotating addresses. Must be chained after RequireJWT (it calls
// MustClaims).
func (r *RateLimit) ByUser() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims := MustClaims(c)
			if !r.allow(claims.UserID.String()) {
				return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
			}
			return next(c)
		}
	}
}

func (r *RateLimit) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if reset, ok := r.resetAt[key]; !ok || now.After(reset) {
		r.counts[key] = 0
		r.resetAt[key] = now.Add(r.window)
	}
	if r.counts[key] >= r.budget {
		return false
	}
	r.counts[key]++
	return true
}
