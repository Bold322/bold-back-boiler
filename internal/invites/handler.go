package invites

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/middleware"
)

const maxBulkInviteRows = 1000

type Handler struct {
	svc      *Service
	ttlHours int
	baseURL  string
}

func NewHandler(svc *Service, ttlHours int, baseURL string) *Handler {
	return &Handler{svc: svc, ttlHours: ttlHours, baseURL: baseURL}
}

type issueReq struct {
	Email    string      `json:"email"`
	UserType string      `json:"user_type"`
	LevelIDs []uuid.UUID `json:"level_ids"`
}

type issueResp struct {
	ID        uuid.UUID   `json:"id"`
	Email     string      `json:"email"`
	UserType  string      `json:"user_type"`
	LevelIDs  []uuid.UUID `json:"level_ids"`
	ExpiresAt string      `json:"expires_at"`
}

func (h *Handler) Issue(c echo.Context) error {
	claims := middleware.MustClaims(c)
	var in issueReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Email == "" || in.UserType == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email and user_type required"})
	}
	out, err := h.svc.Issue(c.Request().Context(), IssueInput{
		TenantID: claims.TenantID, Email: in.Email, UserType: in.UserType, LevelIDs: in.LevelIDs,
		InviterID: claims.UserID, TTLHours: h.ttlHours, BaseURL: h.baseURL,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidLevelIDs):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrInvitePending):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "issue invite failed"})
		}
	}
	return c.JSON(http.StatusCreated, issueResp{
		ID: out.Invite.ID, Email: out.Invite.Email,
		UserType: out.Invite.UserType, LevelIDs: out.Invite.LevelIDs,
		ExpiresAt: out.Invite.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

type skippedInvite struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

type bulkInviteResp struct {
	LevelID   uuid.UUID       `json:"level_id"`
	LevelName string          `json:"level_name"`
	Total     int             `json:"total"`
	Invited   []string        `json:"invited"`
	Skipped   []skippedInvite `json:"skipped"`
}

// IssueBulk invites every email in an uploaded CSV as a worker at the tenant's
// default level. The CSV arrives either as a multipart "file" field or the raw
// request body; one email per row, first column only. The batch never aborts:
// per-email outcomes are reported in the response.
func (h *Handler) IssueBulk(c echo.Context) error {
	claims := middleware.MustClaims(c)
	emails, perr := parseInviteCSV(c)
	if perr != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": perr.Error()})
	}
	if len(emails) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "no valid email rows found in CSV"})
	}
	if len(emails) > maxBulkInviteRows {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("too many rows: %d (max %d)", len(emails), maxBulkInviteRows),
		})
	}
	out, err := h.svc.IssueBulk(c.Request().Context(), BulkIssueInput{
		TenantID: claims.TenantID, Emails: emails, InviterID: claims.UserID,
		TTLHours: h.ttlHours, BaseURL: h.baseURL,
	})
	if err != nil {
		if errors.Is(err, ErrNoDefaultLevel) {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "bulk invite failed"})
	}
	resp := bulkInviteResp{
		LevelID:   out.LevelID,
		LevelName: out.LevelName,
		Total:     len(out.Invited) + len(out.Skipped),
		Invited:   out.Invited,
		Skipped:   make([]skippedInvite, 0, len(out.Skipped)),
	}
	if resp.Invited == nil {
		resp.Invited = []string{}
	}
	for _, s := range out.Skipped {
		resp.Skipped = append(resp.Skipped, skippedInvite{Email: s.Email, Reason: s.Reason})
	}
	return c.JSON(http.StatusOK, resp)
}

// parseInviteCSV reads the email list from either a multipart "file" field or
// the raw request body, then delegates parsing to parseEmailCSV.
func parseInviteCSV(c echo.Context) ([]string, error) {
	if fh, err := c.FormFile("file"); err == nil {
		f, oerr := fh.Open()
		if oerr != nil {
			return nil, fmt.Errorf("open upload: %w", oerr)
		}
		defer f.Close()
		return parseEmailCSV(f)
	}
	return parseEmailCSV(c.Request().Body)
}

// parseEmailCSV extracts emails from a CSV stream: first column only, a leading
// "email"/"emails" header row is skipped, values are trimmed, lowercased, and
// de-duplicated. Rows that net/mail rejects are dropped silently; the response
// reports the accepted set so the caller sees exactly who was invited.
func parseEmailCSV(r io.Reader) ([]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	seen := map[string]struct{}{}
	out := []string{}
	first := true
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse csv: %w", err)
		}
		if len(rec) == 0 {
			continue
		}
		val := strings.ToLower(strings.TrimSpace(rec[0]))
		if first {
			first = false
			if val == "email" || val == "emails" {
				continue
			}
		}
		if val == "" {
			continue
		}
		if _, dup := seen[val]; dup {
			continue
		}
		if _, err := mail.ParseAddress(val); err != nil {
			continue
		}
		seen[val] = struct{}{}
		out = append(out, val)
	}
	return out, nil
}

func (h *Handler) List(c echo.Context) error {
	claims := middleware.MustClaims(c)
	out, err := h.svc.ListPending(c.Request().Context(), claims.TenantID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "list invites failed"})
	}
	resp := make([]issueResp, 0, len(out))
	for _, inv := range out {
		resp = append(resp, issueResp{
			ID: inv.ID, Email: inv.Email,
			UserType: inv.UserType, LevelIDs: inv.LevelIDs,
			ExpiresAt: inv.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"invites": resp})
}

func (h *Handler) Revoke(c echo.Context) error {
	claims := middleware.MustClaims(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	if err := h.svc.Revoke(c.Request().Context(), claims.TenantID, id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "revoke failed"})
	}
	return c.NoContent(http.StatusNoContent)
}

type acceptReq struct {
	Token       string `json:"token"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type acceptResp struct {
	Token  string    `json:"token"`
	UserID uuid.UUID `json:"user_id"`
}

// AcceptHandler is the public endpoint that consumes an invite token and
// returns a fresh JWT for the newly created user. It lives in this package
// (not internal/auth) to avoid an import cycle: invites imports auth for
// HashPassword, so auth must not import invites.
type AcceptHandler struct {
	svc     *Service
	authSvc *auth.Service
}

func NewAcceptHandler(svc *Service, authSvc *auth.Service) *AcceptHandler {
	return &AcceptHandler{svc: svc, authSvc: authSvc}
}

func (h *AcceptHandler) Accept(c echo.Context) error {
	var in acceptReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Token == "" || in.Password == "" || in.DisplayName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "token, password, display_name required"})
	}
	out, err := h.svc.Accept(c.Request().Context(), AcceptInput{
		Token: in.Token, Password: in.Password, DisplayName: in.DisplayName,
	})
	if err != nil {
		if errors.Is(err, ErrInviteExpired) {
			return c.JSON(http.StatusGone, map[string]string{"error": "invite expired"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "accept failed"})
	}
	sess, err := h.authSvc.MintSession(c.Request().Context(), out.UserID, out.TenantID, out.UserType, out.LevelIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "sign failed"})
	}
	h.authSvc.SetRefreshCookie(c, sess.RefreshToken, sess.RefreshExpiresAt)
	return c.JSON(http.StatusCreated, acceptResp{Token: sess.AccessToken, UserID: out.UserID})
}

func (h *Handler) Resend(c echo.Context) error {
	claims := middleware.MustClaims(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid id"})
	}
	out, err := h.svc.Resend(c.Request().Context(), claims.TenantID, id, h.ttlHours, h.baseURL)
	if err != nil {
		switch {
		case errors.Is(err, ErrInviteNotFound):
			return c.JSON(http.StatusNotFound, map[string]string{"error": "invite not found"})
		case errors.Is(err, ErrInviteExpired):
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "resend failed"})
		}
	}
	return c.JSON(http.StatusOK, issueResp{
		ID: out.Invite.ID, Email: out.Invite.Email,
		UserType: out.Invite.UserType, LevelIDs: out.Invite.LevelIDs,
		ExpiresAt: out.Invite.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}
