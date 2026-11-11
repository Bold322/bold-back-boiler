package superadmin

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

// Announcement matches the FE's super-admin Announcement shape verbatim
// (camelCase JSON) so the seam can deserialize without an extra mapper.
// Status is derived from starts_at/ends_at — never stored.
type Announcement struct {
	ID              uuid.UUID   `json:"id"`
	Title           string      `json:"title"`
	Body            string      `json:"body"`
	Severity        string      `json:"severity"`
	Target          string      `json:"target"`
	TargetTier      *string     `json:"targetTier,omitempty"`
	TargetTenantIDs []uuid.UUID `json:"targetTenantIds,omitempty"`
	StartsAt        time.Time   `json:"startsAt"`
	EndsAt          *time.Time  `json:"endsAt,omitempty"`
	Status          string      `json:"status"`
	CreatedBy       uuid.UUID   `json:"createdBy"`
}

const announcementsListLimit = 200

// ListAnnouncements returns up to 200 newest-first system announcements with
// status derived from the schedule. Capped low because the super-admin console
// renders the full list client-side; pagination can come later.
func (s *Service) ListAnnouncements(ctx context.Context) ([]Announcement, error) {
	rows, err := s.q.ListSystemAnnouncements(ctx, announcementsListLimit)
	if err != nil {
		return nil, err
	}
	out := make([]Announcement, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapAnnouncement(r))
	}
	return out, nil
}

type CreateAnnouncementInput struct {
	Title           string
	Body            string
	Severity        string
	Target          string
	TargetTier      *string
	TargetTenantIDs []uuid.UUID
	StartsAt        time.Time
	EndsAt          *time.Time
	CreatedBy       uuid.UUID
}

func (s *Service) CreateAnnouncement(ctx context.Context, in CreateAnnouncementInput) (Announcement, error) {
	row, err := s.q.CreateSystemAnnouncement(ctx, sqlc.CreateSystemAnnouncementParams{
		Title: in.Title, Body: in.Body, Severity: in.Severity, Target: in.Target,
		TargetTier:      in.TargetTier,
		TargetTenantIds: in.TargetTenantIDs,
		StartsAt:        pgtype.Timestamptz{Time: in.StartsAt, Valid: true},
		EndsAt:          pgTime(in.EndsAt),
		CreatedBy:       in.CreatedBy,
	})
	if err != nil {
		return Announcement{}, err
	}
	return mapAnnouncement(row), nil
}

func (s *Service) DeleteAnnouncement(ctx context.Context, id uuid.UUID) error {
	rows, err := s.q.DeleteSystemAnnouncement(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrTenantNotFound // reuse the same not-found sentinel
	}
	return nil
}

func deriveStatus(starts time.Time, ends *time.Time) string {
	now := time.Now()
	if starts.After(now) {
		return "scheduled"
	}
	if ends != nil && !ends.After(now) {
		return "ended"
	}
	return "live"
}

func mapAnnouncement(r sqlc.SystemAnnouncement) Announcement {
	var endsAt *time.Time
	if r.EndsAt.Valid {
		t := r.EndsAt.Time
		endsAt = &t
	}
	return Announcement{
		ID:              r.ID,
		Title:           r.Title,
		Body:            r.Body,
		Severity:        r.Severity,
		Target:          r.Target,
		TargetTier:      r.TargetTier,
		TargetTenantIDs: r.TargetTenantIds,
		StartsAt:        r.StartsAt.Time,
		EndsAt:          endsAt,
		Status:          deriveStatus(r.StartsAt.Time, endsAt),
		CreatedBy:       r.CreatedBy,
	}
}

func pgTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// ---------- Handler methods ----------

func (h *Handler) ListAnnouncements(c echo.Context) error {
	out, err := h.svc.ListAnnouncements(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"announcements": out})
}

type createAnnouncementReq struct {
	Title           string      `json:"title"`
	Body            string      `json:"body"`
	Severity        string      `json:"severity"`
	Target          string      `json:"target"`
	TargetTier      *string     `json:"targetTier,omitempty"`
	TargetTenantIDs []uuid.UUID `json:"targetTenantIds,omitempty"`
	StartsAt        time.Time   `json:"startsAt"`
	EndsAt          *time.Time  `json:"endsAt,omitempty"`
}

func (h *Handler) CreateAnnouncement(c echo.Context) error {
	claims := middleware.MustClaims(c)
	var in createAnnouncementReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Title == "" || in.Body == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "title and body required"})
	}
	switch in.Severity {
	case "info", "warning", "maintenance":
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "severity must be info|warning|maintenance"})
	}
	switch in.Target {
	case "all", "tier", "tenants":
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "target must be all|tier|tenants"})
	}
	if in.StartsAt.IsZero() {
		in.StartsAt = time.Now()
	}
	out, err := h.svc.CreateAnnouncement(c.Request().Context(), CreateAnnouncementInput{
		Title: in.Title, Body: in.Body, Severity: in.Severity, Target: in.Target,
		TargetTier: in.TargetTier, TargetTenantIDs: in.TargetTenantIDs,
		StartsAt: in.StartsAt, EndsAt: in.EndsAt, CreatedBy: claims.UserID,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create failed"})
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *Handler) DeleteAnnouncement(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "bad id"})
	}
	if err := h.svc.DeleteAnnouncement(c.Request().Context(), id); err != nil {
		if err == ErrTenantNotFound {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete failed"})
	}
	return c.NoContent(http.StatusNoContent)
}
