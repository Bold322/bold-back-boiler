package superadmin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// ServiceHealth mirrors the frontend's ServiceHealth shape.
type ServiceHealth struct {
	Status       string `json:"status"` // healthy | degraded | down
	P95LatencyMs int64  `json:"p95LatencyMs"`
	Detail       string `json:"detail,omitempty"`
}

// ExtraServiceHealth is a product-added service (queue, cache, 3rd party).
type ExtraServiceHealth struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	ServiceHealth
}

// SystemHealth is the payload of GET /superadmin/system/health.
type SystemHealth struct {
	API      ServiceHealth        `json:"api"`
	Database ServiceHealth        `json:"database"`
	Email    ServiceHealth        `json:"email"`
	Extra    []ExtraServiceHealth `json:"extra"`
}

// SetEmailDelivery records whether outbound email is "on" (SES) or "off"
// (noop logger) so the health view can report it.
func (h *Handler) SetEmailDelivery(mode string) { h.emailDelivery = mode }

// GetSystemHealth pings the database and reports a coarse status for the
// API, the database and email delivery. Extend `Extra` with any service the
// product adds (queues, caches, external APIs).
func (h *Handler) GetSystemHealth(c echo.Context) error {
	ctx := c.Request().Context()

	db := ServiceHealth{Status: "healthy"}
	start := time.Now()
	if err := h.svc.pool.Ping(ctx); err != nil {
		db.Status = "down"
		db.Detail = err.Error()
	}
	db.P95LatencyMs = time.Since(start).Milliseconds()
	if db.Status == "healthy" {
		st := h.svc.pool.Stat()
		db.Detail = "pool " + strconv.Itoa(int(st.AcquiredConns())) + "/" + strconv.Itoa(int(st.MaxConns()))
	}

	email := ServiceHealth{Status: "healthy", Detail: "SES"}
	if h.emailDelivery != "on" {
		email.Detail = "noop (EMAIL_DELIVERY=off)"
	}

	api := ServiceHealth{Status: "healthy"}
	if db.Status == "down" {
		api.Status = "degraded"
	}

	return c.JSON(http.StatusOK, SystemHealth{
		API: api, Database: db, Email: email, Extra: []ExtraServiceHealth{},
	})
}
