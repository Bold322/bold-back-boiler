package admin

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

// TenantStats is the JSON shape served by GET /admin/stats: the generic
// counters every tenant has. Extend GetTenantStats (queries/admin.sql) with
// your own domain counters and add fields here.
type TenantStats struct {
	TotalMembers   int32 `json:"totalMembers"`
	AdminMembers   int32 `json:"adminMembers"`
	PendingInvites int32 `json:"pendingInvites"`
	Departments    int32 `json:"departments"`
}

// GetTenantStats fetches the admin overview snapshot for one tenant.
func (s *Service) GetTenantStats(ctx context.Context, tenantID uuid.UUID) (TenantStats, error) {
	row, err := s.q.GetTenantStats(ctx, tenantID)
	if err != nil {
		return TenantStats{}, err
	}
	return TenantStats{
		TotalMembers:   row.TotalMembers,
		AdminMembers:   row.AdminMembers,
		PendingInvites: row.PendingInvites,
		Departments:    row.Departments,
	}, nil
}

// ----- handler methods -----

func (h *Handler) Stats(c echo.Context) error {
	claims := middleware.MustClaims(c)
	out, err := h.svc.GetTenantStats(c.Request().Context(), claims.TenantID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "stats failed"})
	}
	return c.JSON(http.StatusOK, out)
}
