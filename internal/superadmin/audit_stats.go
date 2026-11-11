package superadmin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

// SuperAuditLogEntry matches the FE shape. Actor kind is heuristic:
// 'super-admin' if email == known superadmin (we'd need to query users.user_type
// for the real check; deferred), else 'tenant-admin'.
type SuperAuditLogEntry struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	TenantName string    `json:"tenantName"`
	Actor      string    `json:"actor"`
	ActorKind  string    `json:"actorKind"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource,omitempty"`
	Severity   string    `json:"severity"`
	CreatedAt  time.Time `json:"createdAt"`
}

const defaultAuditLimit = 200

func (s *Service) ListSuperAudit(ctx context.Context, tenantFilter *uuid.UUID, limit int) ([]SuperAuditLogEntry, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	rows, err := s.q.ListSuperAudit(ctx, sqlc.ListSuperAuditParams{
		TenantFilter: tenantFilter, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]SuperAuditLogEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, SuperAuditLogEntry{
			ID: r.ID, TenantID: r.TenantID.String(), TenantName: r.TenantName,
			Actor: r.ActorEmail, ActorKind: "tenant-admin",
			Action: r.Action, Resource: r.Resource, Severity: r.Severity,
			CreatedAt: r.At.Time,
		})
	}
	return out, nil
}

// SuperAdminStats matches the FE shape. tier-distribution and MRR rely on
// the tenants table having a `tier` column (added in #42); until then we
// emit zeros for tier counts and MRR.
type SuperAdminStats struct {
	TotalTenants      int            `json:"totalTenants"`
	ActiveTenants     int            `json:"activeTenants"`
	OnboardingTenants int            `json:"onboardingTenants"`
	TotalMrrMnt       int64          `json:"totalMrrMnt"`
	TrialTenants      int            `json:"trialTenants"`
	TotalSeatsUsed    int            `json:"totalSeatsUsed"`
	TenantsByTier     map[string]int `json:"tenantsByTier"`
}

func (s *Service) GetPlatformStats(ctx context.Context) (SuperAdminStats, error) {
	row, err := s.q.GetPlatformStats(ctx)
	if err != nil {
		return SuperAdminStats{}, err
	}
	return SuperAdminStats{
		TotalTenants:      int(row.TotalTenants),
		ActiveTenants:     int(row.ActiveTenants),
		OnboardingTenants: 0,
		TotalMrrMnt:       0,
		TrialTenants:      0,
		TotalSeatsUsed:    int(row.TotalMembers),
		TenantsByTier:     map[string]int{"starter": int(row.ActiveTenants), "growth": 0, "business": 0, "enterprise": 0},
	}, nil
}

// InternalUser matches the FE shape. lastActiveAt is set to created_at for
// now; a real "last active" needs login-audit which we don't track yet.
type InternalUser struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	LastActiveAt time.Time `json:"lastActiveAt"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (s *Service) ListInternalUsers(ctx context.Context) ([]InternalUser, error) {
	rows, err := s.q.ListPlatformTenantUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InternalUser, 0, len(rows))
	for _, r := range rows {
		out = append(out, InternalUser{
			ID: r.ID.String(), Name: r.DisplayName, Email: r.Email,
			Role: "super-admin", LastActiveAt: r.CreatedAt.Time, CreatedAt: r.CreatedAt.Time,
		})
	}
	return out, nil
}

// ---------- Handler ----------

func (h *Handler) ListSuperAudit(c echo.Context) error {
	limit := defaultAuditLimit
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	var tenantFilter *uuid.UUID
	if v := c.QueryParam("tenant_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			tenantFilter = &id
		}
	}
	out, err := h.svc.ListSuperAudit(c.Request().Context(), tenantFilter, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "audit failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": out})
}

func (h *Handler) GetPlatformStats(c echo.Context) error {
	out, err := h.svc.GetPlatformStats(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "stats failed"})
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handler) ListInternalUsers(c echo.Context) error {
	out, err := h.svc.ListInternalUsers(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"users": out})
}
