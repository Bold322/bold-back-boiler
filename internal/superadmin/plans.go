package superadmin

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

// PlanDefinition mirrors the FE's PlanDefinition shape. Nullable fields use
// pointer types so they serialize to JSON null (rather than 0), matching the
// FE which treats null as "unlimited" for seat/storage/upload caps.
type PlanDefinition struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	PriceMnt        *int64  `json:"priceMnt"`
	TokenLimit      int64   `json:"tokenLimit"`
	SeatLimit       *int32  `json:"seatLimit"`
	StorageLimitGb  *int32  `json:"storageLimitGb"`
	UploadsPerMonth *int32  `json:"uploadsPerMonth"`
	Notes           *string `json:"notes,omitempty"`
}

func mapPlan(r sqlc.TenantPlan) PlanDefinition {
	return PlanDefinition{
		ID: r.ID, Name: r.Name,
		PriceMnt: r.PriceMnt, TokenLimit: r.TokenLimit,
		SeatLimit: r.SeatLimit, StorageLimitGb: r.StorageLimitGb,
		UploadsPerMonth: r.UploadsPerMonth, Notes: r.Notes,
	}
}

func (h *Handler) ListPlans(c echo.Context) error {
	rows, err := h.svc.q.ListTenantPlans(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	out := make([]PlanDefinition, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapPlan(r))
	}
	return c.JSON(http.StatusOK, map[string]any{"plans": out})
}

// patchPlanReq uses pointers (and explicit "*Set" siblings derived from
// pointer-non-nil) to distinguish "not provided" from "set to null". The FE's
// updatePlan sends only the fields the user changed.
type patchPlanReq struct {
	Name                *string `json:"name,omitempty"`
	PriceMnt            *int64  `json:"priceMnt,omitempty"`
	PriceMntNull        bool    `json:"priceMntNull,omitempty"`
	TokenLimit          *int64  `json:"tokenLimit,omitempty"`
	SeatLimit           *int32  `json:"seatLimit,omitempty"`
	SeatLimitNull       bool    `json:"seatLimitNull,omitempty"`
	StorageLimitGb      *int32  `json:"storageLimitGb,omitempty"`
	StorageLimitGbNull  bool    `json:"storageLimitGbNull,omitempty"`
	UploadsPerMonth     *int32  `json:"uploadsPerMonth,omitempty"`
	UploadsPerMonthNull bool    `json:"uploadsPerMonthNull,omitempty"`
	Notes               *string `json:"notes,omitempty"`
	NotesNull           bool    `json:"notesNull,omitempty"`
}

func (h *Handler) UpdatePlan(c echo.Context) error {
	id := c.Param("id")
	switch id {
	case "starter", "growth", "business", "enterprise":
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid plan id"})
	}
	var in patchPlanReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	row, err := h.svc.q.UpdateTenantPlan(c.Request().Context(), sqlc.UpdateTenantPlanParams{
		ID:                 id,
		Name:               in.Name,
		PriceMntSet:        ptrBool(in.PriceMnt != nil || in.PriceMntNull),
		PriceMnt:           in.PriceMnt,
		TokenLimit:         in.TokenLimit,
		SeatLimitSet:       ptrBool(in.SeatLimit != nil || in.SeatLimitNull),
		SeatLimit:          in.SeatLimit,
		StorageLimitGbSet:  ptrBool(in.StorageLimitGb != nil || in.StorageLimitGbNull),
		StorageLimitGb:     in.StorageLimitGb,
		UploadsPerMonthSet: ptrBool(in.UploadsPerMonth != nil || in.UploadsPerMonthNull),
		UploadsPerMonth:    in.UploadsPerMonth,
		NotesSet:           ptrBool(in.Notes != nil || in.NotesNull),
		Notes:              in.Notes,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update failed"})
	}
	return c.JSON(http.StatusOK, mapPlan(row))
}

func ptrBool(b bool) *bool { return &b }
