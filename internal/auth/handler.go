package auth

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

type authResp struct {
	Token string     `json:"token"`
	User  PublicUser `json:"user"`
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) Login(c echo.Context) error {
	var in loginReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if in.Email == "" || in.Password == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}
	out, err := h.svc.Login(c.Request().Context(), in.Email, in.Password)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}
	h.svc.SetRefreshCookie(c, out.RefreshToken, out.RefreshExpiresAt)
	return c.JSON(http.StatusOK, authResp{Token: out.Token, User: out.User})
}

type updateMeReq struct {
	DisplayName     string `json:"display_name,omitempty"`
	NewPassword     string `json:"new_password,omitempty"`
	CurrentPassword string `json:"current_password,omitempty"`
}

// Me returns the authenticated user's public profile. The caller (router)
// is responsible for extracting userID from the request claims; auth handlers
// can't reach into the middleware package without creating an import cycle.
func (h *Handler) Me(c echo.Context, userID uuid.UUID) error {
	out, err := h.svc.GetPublicUser(c.Request().Context(), userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "fetch failed"})
	}
	return c.JSON(http.StatusOK, out)
}

type forgotPasswordReq struct {
	Email string `json:"email"`
}

// ForgotPassword always responds 200 {ok:true} so the public API can't be
// used to enumerate registered emails. Real delivery happens asynchronously
// via the configured email sender; the noop sender just logs.
func (h *Handler) ForgotPassword(c echo.Context) error {
	var in forgotPasswordReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if err := h.svc.RequestPasswordReset(c.Request().Context(), in.Email); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "reset failed"})
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

type resetPasswordReq struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ResetPassword consumes a single-use token and sets a new password. The
// caller is not logged in — they get a token via email. A new login is
// required after this succeeds.
func (h *Handler) ResetPassword(c echo.Context) error {
	var in resetPasswordReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	if err := h.svc.ResetPassword(c.Request().Context(), in.Token, in.NewPassword); err != nil {
		switch {
		case errors.Is(err, ErrResetTokenInvalid):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrPasswordTooShort):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "reset failed"})
		}
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// Refresh rotates the httpOnly refresh cookie and returns a fresh access token.
// Public route (no bearer needed); protected by a required custom header
// (double-submit style, layered on the cookie's SameSite attribute).
func (h *Handler) Refresh(c echo.Context) error {
	if c.Request().Header.Get("X-Refresh-Request") == "" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "missing refresh header"})
	}
	ck, err := c.Cookie(h.svc.RefreshCookieName())
	if err != nil || ck.Value == "" {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "no session"})
	}
	out, err := h.svc.Refresh(c.Request().Context(), ck.Value)
	if err != nil {
		h.svc.ClearRefreshCookie(c)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "session expired"})
	}
	h.svc.SetRefreshCookie(c, out.RefreshToken, out.RefreshExpiresAt)
	return c.JSON(http.StatusOK, map[string]string{"token": out.AccessToken})
}

// Logout revokes the presented refresh family and clears the cookie. Idempotent.
func (h *Handler) Logout(c echo.Context) error {
	if c.Request().Header.Get("X-Refresh-Request") == "" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "missing refresh header"})
	}
	if ck, err := c.Cookie(h.svc.RefreshCookieName()); err == nil {
		if err := h.svc.Logout(c.Request().Context(), ck.Value); err != nil {
			slog.Error("logout: revoke failed", "err", err.Error())
		}
	}
	h.svc.ClearRefreshCookie(c)
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// UpdateMe lets the authenticated user change their display name and/or password.
func (h *Handler) UpdateMe(c echo.Context, userID, sessionID uuid.UUID) error {
	var in updateMeReq
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "malformed body"})
	}
	out, err := h.svc.UpdateMe(c.Request().Context(), userID, sessionID, UpdateMeInput{
		DisplayName:     in.DisplayName,
		NewPassword:     in.NewPassword,
		CurrentPassword: in.CurrentPassword,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "current password incorrect"})
		case errors.Is(err, ErrPasswordRequired):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrPasswordTooShort):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "update failed"})
		}
	}
	return c.JSON(http.StatusOK, out)
}
