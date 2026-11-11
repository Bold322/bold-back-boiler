package superadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

type PatchTenantInput struct {
	Name  *string
	Tier  *string
	Flags map[string]bool
}

// PatchTenantOutput mirrors the FE Tenant placeholder shape closely enough
// for the seam to consume directly. The FE's mapBackendTenant ultimately
// adapts the wire shape into its richer Tenant interface.
type PatchTenantOutput struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Slug      string          `json:"slug"`
	Status    string          `json:"status"`
	Tier      string          `json:"tier"`
	Flags     map[string]bool `json:"flags"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func (s *Service) PatchTenant(ctx context.Context, id uuid.UUID, in PatchTenantInput) (PatchTenantOutput, error) {
	var flagsBytes []byte
	if in.Flags != nil {
		b, err := json.Marshal(in.Flags)
		if err != nil {
			return PatchTenantOutput{}, err
		}
		flagsBytes = b
	}
	row, err := s.q.PatchTenant(ctx, sqlc.PatchTenantParams{
		ID: id, Name: in.Name, Tier: in.Tier, Flags: flagsBytes,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PatchTenantOutput{}, ErrTenantNotFound
		}
		return PatchTenantOutput{}, err
	}
	flagsOut := map[string]bool{}
	if len(row.Flags) > 0 {
		_ = json.Unmarshal(row.Flags, &flagsOut)
	}
	return PatchTenantOutput{
		ID: row.ID, Name: row.Name, Slug: row.Slug, Status: row.Status,
		Tier: row.Tier, Flags: flagsOut,
		CreatedAt: row.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z"),
	}, nil
}

type patchTenantReq struct {
	Name  *string         `json:"name,omitempty"`
	Tier  *string         `json:"tier,omitempty"`
	Flags map[string]bool `json:"flags,omitempty"`
}

func (h *Handler) PatchTenant(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "bad id"})
	}
	var in patchTenantReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Tier != nil {
		switch *in.Tier {
		case "starter", "growth", "business", "enterprise":
		default:
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "tier must be starter|growth|business|enterprise"})
		}
	}
	out, err := h.svc.PatchTenant(c.Request().Context(), id, PatchTenantInput{
		Name: in.Name, Tier: in.Tier, Flags: in.Flags,
	})
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "patch failed"})
	}
	return c.JSON(http.StatusOK, out)
}
