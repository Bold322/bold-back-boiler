// Package preferences exposes per-user notification and locale preferences
// via GET/PATCH /preferences. There's no service layer yet — the handler is
// a thin wrapper around two sqlc queries (get + upsert).
package preferences

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

// Defaults mirror the migration column defaults and the FE's
// DEFAULT_PREFERENCES so an unset user still gets a consistent shape.
var Defaults = Preferences{
	Language:       "mn",
	EmailDigest:    true,
	MentionAlerts:  true,
	DocReadyAlerts: false,
}

type Preferences struct {
	Language       string `json:"language"`
	EmailDigest    bool   `json:"emailDigest"`
	MentionAlerts  bool   `json:"mentionAlerts"`
	DocReadyAlerts bool   `json:"docReadyAlerts"`
}

type Handler struct {
	q *sqlc.Queries
}

func NewHandler(q *sqlc.Queries) *Handler {
	return &Handler{q: q}
}

func (h *Handler) Get(c echo.Context) error {
	claims := middleware.MustClaims(c)
	row, err := h.q.GetUserPreferences(c.Request().Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusOK, Defaults)
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "load failed"})
	}
	return c.JSON(http.StatusOK, Preferences{
		Language: row.Language, EmailDigest: row.EmailDigest,
		MentionAlerts: row.MentionAlerts, DocReadyAlerts: row.DocReadyAlerts,
	})
}

// patchReq fields are pointers so the JSON decoder can distinguish "not
// provided" (nil → keep existing) from "explicitly set to false" (&false →
// store). The sqlc upsert COALESCEs nulls back to either the existing row
// or the default, which keeps the wire shape minimal.
type patchReq struct {
	Language       *string `json:"language,omitempty"`
	EmailDigest    *bool   `json:"emailDigest,omitempty"`
	MentionAlerts  *bool   `json:"mentionAlerts,omitempty"`
	DocReadyAlerts *bool   `json:"docReadyAlerts,omitempty"`
}

func (h *Handler) Patch(c echo.Context) error {
	claims := middleware.MustClaims(c)
	var in patchReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	row, err := h.q.UpsertUserPreferences(c.Request().Context(), sqlc.UpsertUserPreferencesParams{
		UserID:         claims.UserID,
		TenantID:       claims.TenantID,
		Language:       in.Language,
		EmailDigest:    in.EmailDigest,
		MentionAlerts:  in.MentionAlerts,
		DocReadyAlerts: in.DocReadyAlerts,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "save failed"})
	}
	return c.JSON(http.StatusOK, Preferences{
		Language: row.Language, EmailDigest: row.EmailDigest,
		MentionAlerts: row.MentionAlerts, DocReadyAlerts: row.DocReadyAlerts,
	})
}
