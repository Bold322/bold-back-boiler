package superadmin

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/admin"
	"github.com/Bold322/bold-back-boiler/internal/invites"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
)

type Handler struct {
	svc           *Service
	inviteTTLHrs  int
	publicBaseURL string
	restoreGrace  int
	emailDelivery string
}

func NewHandler(svc *Service, inviteTTLHours, restoreGraceHours int, publicBaseURL string) *Handler {
	return &Handler{
		svc: svc, inviteTTLHrs: inviteTTLHours,
		publicBaseURL: publicBaseURL, restoreGrace: restoreGraceHours,
	}
}

func (h *Handler) ListTenants(c echo.Context) error {
	include := c.QueryParam("include_deleted") == "true"
	out, err := h.svc.ListTenants(c.Request().Context(), include)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"tenants": out})
}

type createTenantReq struct {
	Name              string `json:"name"`
	Slug              string `json:"slug"`
	InitialAdminEmail string `json:"initial_admin_email"`
}

type createTenantResp struct {
	TenantID    uuid.UUID `json:"tenant_id"`
	InviteToken string    `json:"invite_token"`
	AcceptURL   string    `json:"accept_url"`
}

func (h *Handler) CreateTenant(c echo.Context) error {
	var in createTenantReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	out, err := h.svc.CreateTenant(c.Request().Context(), tenants.CreateTenantInput{
		Name:              in.Name,
		Slug:              in.Slug,
		InitialAdminEmail: in.InitialAdminEmail,
		InviteTTLHours:    h.inviteTTLHrs,
		PublicBaseURL:     h.publicBaseURL,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAdminEmailRequired):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, tenants.ErrSlugInvalid):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, tenants.ErrSlugTaken):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create failed"})
		}
	}
	return c.JSON(http.StatusCreated, createTenantResp{
		TenantID: out.Tenant.ID, InviteToken: out.InviteToken, AcceptURL: out.AcceptURL,
	})
}

func (h *Handler) SuspendTenant(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.svc.requireExists(c.Request().Context(), id); err != nil {
		return tenantErrToStatus(c, err)
	}
	if err := h.svc.SuspendTenant(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "suspend failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) UnsuspendTenant(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.svc.requireExists(c.Request().Context(), id); err != nil {
		return tenantErrToStatus(c, err)
	}
	if err := h.svc.UnsuspendTenant(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "unsuspend failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) DeleteTenant(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.svc.requireExists(c.Request().Context(), id); err != nil {
		return tenantErrToStatus(c, err)
	}
	if err := h.svc.SoftDeleteTenant(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) RestoreTenant(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.svc.requireExists(c.Request().Context(), id); err != nil {
		return tenantErrToStatus(c, err)
	}
	if err := h.svc.RestoreTenant(c.Request().Context(), id, h.restoreGrace); err != nil {
		if errors.Is(err, tenants.ErrRestoreExpired) {
			return c.JSON(http.StatusGone, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "restore failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

func tenantErrToStatus(c echo.Context, err error) error {
	if errors.Is(err, ErrTenantNotFound) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// Cross-tenant member endpoints ----------------------------------------

func (h *Handler) ListUsersInTenant(c echo.Context) error {
	tid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
	}
	includeDeleted := c.QueryParam("include_deleted") == "true"
	out, err := h.svc.ListUsersInTenant(c.Request().Context(), tid, c.QueryParam("q"), includeDeleted)
	if err != nil {
		return tenantErrToStatus(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"users": out})
}

type inviteUserReq struct {
	Email    string      `json:"email"`
	UserType string      `json:"user_type"`
	LevelIDs []uuid.UUID `json:"level_ids"`
}

type inviteUserResp struct {
	InviteID    uuid.UUID `json:"invite_id"`
	Email       string    `json:"email"`
	InviteToken string    `json:"invite_token"`
	AcceptURL   string    `json:"accept_url"`
	ExpiresAt   string    `json:"expires_at"`
}

func (h *Handler) InviteUserInTenant(c echo.Context) error {
	tid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
	}
	var in inviteUserReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Email == "" || in.UserType == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email and user_type required"})
	}
	claims := middleware.MustClaims(c)
	out, err := h.svc.InviteUserInTenant(c.Request().Context(), tid, invites.IssueInput{
		Email: in.Email, UserType: in.UserType, LevelIDs: in.LevelIDs,
		InviterID: claims.UserID, TTLHours: h.inviteTTLHrs, BaseURL: h.publicBaseURL,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrTenantNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, invites.ErrInvalidLevelIDs):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, invites.ErrInvitePending):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "issue invite failed"})
		}
	}
	return c.JSON(http.StatusCreated, inviteUserResp{
		InviteID: out.Invite.ID, Email: out.Invite.Email,
		InviteToken: out.Token, AcceptURL: out.AcceptURL,
		ExpiresAt: out.Invite.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

func (h *Handler) RemoveUserFromTenant(c echo.Context) error {
	tid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
	}
	uid, err := uuid.Parse(c.Param("uid"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	claims := middleware.MustClaims(c)
	if err := h.svc.RemoveUserFromTenant(c.Request().Context(), tid, claims.UserID, uid); err != nil {
		return userLifecycleErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) RestoreUserInTenant(c echo.Context) error {
	tid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid tenant id"})
	}
	uid, err := uuid.Parse(c.Param("uid"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	if err := h.svc.RestoreUserInTenant(c.Request().Context(), tid, uid, h.restoreGrace); err != nil {
		return userLifecycleErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func userLifecycleErr(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrTenantNotFound), errors.Is(err, admin.ErrUserNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, admin.ErrUserAlreadyDeleted), errors.Is(err, admin.ErrRestoreExpired):
		return c.JSON(http.StatusGone, map[string]string{"error": err.Error()})
	case errors.Is(err, admin.ErrUserNotDeleted),
		errors.Is(err, admin.ErrCannotDeleteSelf),
		errors.Is(err, admin.ErrCannotDeleteSuperadmin),
		errors.Is(err, admin.ErrCannotDeleteLastAdmin):
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
