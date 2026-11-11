package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Users -----------------------------------------------------------------

func (h *Handler) List(c echo.Context) error {
	claims := middleware.MustClaims(c)
	includeDeleted := c.QueryParam("include_deleted") == "true"
	out, err := h.svc.ListUsers(c.Request().Context(), claims.TenantID, c.QueryParam("q"), includeDeleted)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"users": out})
}

func (h *Handler) DeleteUser(c echo.Context) error {
	claims := middleware.MustClaims(c)
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	if err := h.svc.DeleteUser(c.Request().Context(), claims.TenantID, claims.UserID, userID); err != nil {
		switch {
		case errors.Is(err, ErrUserNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrUserAlreadyDeleted):
			return c.JSON(http.StatusGone, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrCannotDeleteSelf),
			errors.Is(err, ErrCannotDeleteSuperadmin),
			errors.Is(err, ErrCannotDeleteLastAdmin):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) RestoreUser(c echo.Context) error {
	claims := middleware.MustClaims(c)
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	if err := h.svc.RestoreUser(c.Request().Context(), claims.TenantID, userID, 0); err != nil {
		switch {
		case errors.Is(err, ErrUserNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrUserNotDeleted):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrRestoreExpired):
			return c.JSON(http.StatusGone, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "restore failed"})
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) GrantMembership(c echo.Context) error {
	claims := middleware.MustClaims(c)
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	levelID, err := uuid.Parse(c.Param("level_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid level id"})
	}
	if err := h.svc.GrantMembership(c.Request().Context(), claims.TenantID, claims.UserID, userID, levelID); err != nil {
		switch {
		case errors.Is(err, ErrLevelNotFound), errors.Is(err, ErrDepartmentNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "grant failed"})
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) RevokeMembership(c echo.Context) error {
	claims := middleware.MustClaims(c)
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	levelID, err := uuid.Parse(c.Param("level_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid level id"})
	}
	if err := h.svc.RevokeMembership(c.Request().Context(), claims.TenantID, claims.UserID, userID, levelID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "revoke failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

type setUserTypeReq struct {
	UserType string `json:"user_type"`
}

func (h *Handler) SetUserType(c echo.Context) error {
	claims := middleware.MustClaims(c)
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid user id"})
	}
	var in setUserTypeReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	var serr error
	switch in.UserType {
	case "admin":
		serr = h.svc.PromoteToAdmin(c.Request().Context(), claims.TenantID, claims.UserID, userID)
	case "worker":
		serr = h.svc.DemoteToWorker(c.Request().Context(), claims.TenantID, claims.UserID, userID)
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "user_type must be admin or worker"})
	}
	if serr != nil {
		switch {
		case errors.Is(serr, ErrLastAdmin):
			return c.JSON(http.StatusConflict, map[string]string{"error": serr.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update failed"})
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) ListAudit(c echo.Context) error {
	claims := middleware.MustClaims(c)
	limit := int32(50)
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit must be 1..500"})
		}
		limit = int32(n)
	}
	out, err := h.svc.ListAuditEntries(c.Request().Context(), claims.TenantID, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": out})
}

// Departments -----------------------------------------------------------

type departmentResp struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	DeprecatedAt *time.Time `json:"deprecated_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func toDepartmentResp(d Department) departmentResp {
	return departmentResp{
		ID: d.ID, Name: d.Name, Description: d.Description,
		DeprecatedAt: d.DeprecatedAt, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type createDepartmentReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Handler) CreateDepartment(c echo.Context) error {
	claims := middleware.MustClaims(c)
	var in createDepartmentReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	d, err := h.svc.CreateDepartment(c.Request().Context(), claims.TenantID, claims.UserID, in.Name, in.Description)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidDepartmentName):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrDepartmentAlreadyExists):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create failed"})
		}
	}
	return c.JSON(http.StatusCreated, toDepartmentResp(d))
}

func (h *Handler) ListDepartments(c echo.Context) error {
	claims := middleware.MustClaims(c)
	include := c.QueryParam("include_deprecated") == "true"
	out, err := h.svc.ListDepartments(c.Request().Context(), claims.TenantID, include)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	resp := make([]departmentResp, 0, len(out))
	for _, d := range out {
		resp = append(resp, toDepartmentResp(d))
	}
	return c.JSON(http.StatusOK, map[string]any{"departments": resp})
}

type patchDepartmentReq struct {
	Description *string `json:"description,omitempty"`
	Deprecated  *bool   `json:"deprecated,omitempty"`
}

func (h *Handler) PatchDepartment(c echo.Context) error {
	claims := middleware.MustClaims(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	var in patchDepartmentReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Description == nil && in.Deprecated == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "description or deprecated required"})
	}
	var d Department
	if in.Description != nil {
		d, err = h.svc.UpdateDepartmentDescription(c.Request().Context(), claims.TenantID, claims.UserID, id, *in.Description)
		if err != nil {
			return errToStatus(c, err)
		}
	}
	if in.Deprecated != nil {
		if *in.Deprecated {
			d, err = h.svc.DeprecateDepartment(c.Request().Context(), claims.TenantID, claims.UserID, id)
		} else {
			d, err = h.svc.UndeprecateDepartment(c.Request().Context(), claims.TenantID, claims.UserID, id)
		}
		if err != nil {
			return errToStatus(c, err)
		}
	}
	return c.JSON(http.StatusOK, toDepartmentResp(d))
}

func (h *Handler) DeleteDepartment(c echo.Context) error {
	claims := middleware.MustClaims(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	err = h.svc.DeleteDepartment(c.Request().Context(), claims.TenantID, claims.UserID, id)
	if err == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var inUse *DepartmentInUseError
	if errors.As(err, &inUse) {
		return c.JSON(http.StatusConflict, map[string]any{
			"error":            err.Error(),
			"membership_count": inUse.MembershipCount,
		})
	}
	return errToStatus(c, err)
}

// Levels ----------------------------------------------------------------

type levelResp struct {
	ID           uuid.UUID `json:"id"`
	DepartmentID uuid.UUID `json:"department_id"`
	Name         string    `json:"name"`
	Rank         int32     `json:"rank"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toLevelResp(l Level) levelResp {
	return levelResp{
		ID: l.ID, DepartmentID: l.DepartmentID, Name: l.Name, Rank: l.Rank,
		CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
}

type createLevelReq struct {
	Name string `json:"name"`
	Rank int32  `json:"rank"`
}

func (h *Handler) CreateLevel(c echo.Context) error {
	claims := middleware.MustClaims(c)
	deptID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid department id"})
	}
	var in createLevelReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	l, err := h.svc.CreateLevel(c.Request().Context(), claims.TenantID, claims.UserID, deptID, in.Name, in.Rank)
	if err != nil {
		switch {
		case errors.Is(err, ErrDepartmentNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrInvalidLevelName), errors.Is(err, ErrInvalidRank):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrLevelAlreadyExists):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "create failed"})
		}
	}
	return c.JSON(http.StatusCreated, toLevelResp(l))
}

func (h *Handler) ListLevels(c echo.Context) error {
	claims := middleware.MustClaims(c)
	deptID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid department id"})
	}
	out, err := h.svc.ListLevels(c.Request().Context(), claims.TenantID, deptID)
	if err != nil {
		if errors.Is(err, ErrDepartmentNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	resp := make([]levelResp, 0, len(out))
	for _, l := range out {
		resp = append(resp, toLevelResp(l))
	}
	return c.JSON(http.StatusOK, map[string]any{"levels": resp})
}

type patchLevelReq struct {
	Name *string `json:"name,omitempty"`
	Rank *int32  `json:"rank,omitempty"`
}

func (h *Handler) PatchLevel(c echo.Context) error {
	claims := middleware.MustClaims(c)
	deptID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid department id"})
	}
	levelID, err := uuid.Parse(c.Param("level_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid level id"})
	}
	var in patchLevelReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	l, err := h.svc.UpdateLevel(c.Request().Context(), claims.TenantID, deptID, levelID, UpdateLevelInput{Name: in.Name, Rank: in.Rank})
	if err != nil {
		switch {
		case errors.Is(err, ErrLevelNotFound), errors.Is(err, ErrDepartmentNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrInvalidLevelName), errors.Is(err, ErrInvalidRank):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrLevelAlreadyExists):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "patch failed"})
		}
	}
	return c.JSON(http.StatusOK, toLevelResp(l))
}

func (h *Handler) DeleteLevel(c echo.Context) error {
	claims := middleware.MustClaims(c)
	deptID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid department id"})
	}
	levelID, err := uuid.Parse(c.Param("level_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid level id"})
	}
	err = h.svc.DeleteLevel(c.Request().Context(), claims.TenantID, deptID, levelID)
	if err == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var inUse *LevelInUseError
	if errors.As(err, &inUse) {
		return c.JSON(http.StatusConflict, map[string]any{
			"error":            err.Error(),
			"membership_count": inUse.MembershipCount,
		})
	}
	return errToStatus(c, err)
}

// Public GET /departments (workers + admins) ----------------------------

func (h *Handler) ListDepartmentsPublic(c echo.Context) error {
	claims := middleware.MustClaims(c)
	depts, err := h.svc.ListDepartments(c.Request().Context(), claims.TenantID, false)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
	}
	type publicDeptLevel struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
		Rank int32     `json:"rank"`
	}
	type publicDept struct {
		ID     uuid.UUID         `json:"id"`
		Name   string            `json:"name"`
		Levels []publicDeptLevel `json:"levels"`
	}
	resp := make([]publicDept, 0, len(depts))
	for _, d := range depts {
		levels, err := h.svc.ListLevels(c.Request().Context(), claims.TenantID, d.ID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list failed"})
		}
		pls := make([]publicDeptLevel, 0, len(levels))
		for _, l := range levels {
			pls = append(pls, publicDeptLevel{ID: l.ID, Name: l.Name, Rank: l.Rank})
		}
		resp = append(resp, publicDept{ID: d.ID, Name: d.Name, Levels: pls})
	}
	return c.JSON(http.StatusOK, map[string]any{"departments": resp})
}

// Helpers ---------------------------------------------------------------

func errToStatus(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrDepartmentNotFound), errors.Is(err, ErrLevelNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
