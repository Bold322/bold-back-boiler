package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// Google "Sign in with Google" via the server-side authorization-code flow.
//
// Flow:
//  1. SPA navigates the browser to GET /auth/google/login
//  2. We 302 to Google's consent screen with a signed `state` (carries a
//     nonce + expiry) and set a short-lived state cookie that binds the
//     callback to this browser (CSRF defense).
//  3. Google redirects to GET /auth/google/callback?code=...&state=...
//  4. We verify state (signature, expiry, cookie nonce), exchange the code
//     for tokens using the client secret, read the verified email from the
//     userinfo endpoint, and look up an *existing* user globally by email
//     (users.email is UNIQUE across tenants). No user is ever created here
//     (invite-only invariant).
//  5. On success we redirect the browser to
//     {FRONTEND_BASE_URL}/auth/google/done#token=<jwt> — the token rides in
//     the URL fragment so it never reaches a server log or Referer header.
//     On any failure we redirect to {FRONTEND_BASE_URL}/login?error=<code>.
//
// State is signed with the JWT secret (HMAC-SHA256), not stored, so the API
// stays stateless. The state cookie holds only an opaque nonce.

const (
	googleAuthEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenEndpoint    = "https://oauth2.googleapis.com/token"
	googleUserinfoEndpoint = "https://www.googleapis.com/oauth2/v2/userinfo"

	googleStateCookie = "g_oauth_state"
	googleStateTTL    = 10 * time.Minute
)

// GoogleConfig is the auth package's flat view of the Google settings so this
// package does not import internal/config (server.go does the mapping).
type GoogleConfig struct {
	ClientID        string
	ClientSecret    string
	RedirectURL     string
	FrontendBaseURL string
}

type GoogleHandler struct {
	gc       GoogleConfig
	svc      *Service
	stateKey []byte
	secure   bool
	http     *http.Client
}

// NewGoogleHandler builds the handler. stateKey is the JWT secret bytes
// (reused only to HMAC the OAuth state; it is never put in the state).
func NewGoogleHandler(gc GoogleConfig, svc *Service, stateKey []byte) *GoogleHandler {
	return &GoogleHandler{
		gc:       gc,
		svc:      svc,
		stateKey: stateKey,
		secure:   strings.HasPrefix(strings.ToLower(gc.RedirectURL), "https://"),
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

type googleState struct {
	Nonce string `json:"n"`
	Exp   int64  `json:"e"`
}

func (h *GoogleHandler) signState(s googleState) string {
	payload, _ := json.Marshal(s)
	b := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, h.stateKey)
	mac.Write([]byte(b))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b + "." + sig
}

func (h *GoogleHandler) verifyState(raw string) (googleState, error) {
	var zero googleState
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return zero, errors.New("malformed state")
	}
	mac := hmac.New(sha256.New, h.stateKey)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return zero, errors.New("bad state signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return zero, errors.New("bad state encoding")
	}
	var st googleState
	if err := json.Unmarshal(payload, &st); err != nil {
		return zero, errors.New("bad state payload")
	}
	if time.Now().Unix() > st.Exp {
		return zero, errors.New("state expired")
	}
	return st, nil
}

func (h *GoogleHandler) frontendRedirect(c echo.Context, path string) error {
	return c.Redirect(http.StatusFound, h.gc.FrontendBaseURL+path)
}

// Login starts the OAuth dance. Public route.
func (h *GoogleHandler) Login(c echo.Context) error {
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		slog.Error("google login: rand", "err", err.Error())
		return h.frontendRedirect(c, "/login?error=google_failed")
	}
	nonce := hex.EncodeToString(nb)
	state := h.signState(googleState{
		Nonce: nonce,
		Exp:   time.Now().Add(googleStateTTL).Unix(),
	})

	c.SetCookie(&http.Cookie{
		Name:     googleStateCookie,
		Value:    nonce,
		Path:     "/auth/google",
		MaxAge:   int(googleStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})

	q := url.Values{}
	q.Set("client_id", h.gc.ClientID)
	q.Set("redirect_uri", h.gc.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("include_granted_scopes", "true")
	q.Set("prompt", "select_account")
	return c.Redirect(http.StatusFound, googleAuthEndpoint+"?"+q.Encode())
}

// Callback completes the OAuth dance. Public route. Every failure path is a
// browser redirect back to the SPA login with a stable ?error code; we never
// render JSON here because the user agent is a top-level navigation.
func (h *GoogleHandler) Callback(c echo.Context) error {
	if e := c.QueryParam("error"); e != "" {
		return h.frontendRedirect(c, "/login?error=google_denied")
	}
	code := c.QueryParam("code")
	rawState := c.QueryParam("state")
	if code == "" || rawState == "" {
		return h.frontendRedirect(c, "/login?error=google_failed")
	}

	st, err := h.verifyState(rawState)
	if err != nil {
		slog.Warn("google callback: state", "err", err.Error())
		return h.frontendRedirect(c, "/login?error=google_failed")
	}
	// Bind the callback to the browser that started it.
	ck, err := c.Cookie(googleStateCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(st.Nonce)) != 1 {
		slog.Warn("google callback: state cookie mismatch")
		return h.frontendRedirect(c, "/login?error=google_failed")
	}
	// One-shot: clear the cookie regardless of outcome.
	c.SetCookie(&http.Cookie{
		Name: googleStateCookie, Value: "", Path: "/auth/google",
		MaxAge: -1, HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
	})

	ctx := c.Request().Context()
	accessToken, err := h.exchangeCode(ctx, code)
	if err != nil {
		slog.Error("google callback: token exchange", "err", err.Error())
		return h.frontendRedirect(c, "/login?error=google_failed")
	}
	email, verified, err := h.fetchEmail(ctx, accessToken)
	if err != nil {
		slog.Error("google callback: userinfo", "err", err.Error())
		return h.frontendRedirect(c, "/login?error=google_failed")
	}
	if !verified || email == "" {
		return h.frontendRedirect(c, "/login?error=google_unverified")
	}

	_ = st // state is verified above; no tenant payload to read.
	out, err := h.svc.LoginWithGoogle(ctx, email)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			// No matching user anywhere: invite-only, so we do not
			// provision. Tell the SPA to show "ask your admin for an invite".
			return h.frontendRedirect(c, "/login?error=google_no_account")
		}
		slog.Error("google callback: issue token", "err", err.Error())
		return h.frontendRedirect(c, "/login?error=google_failed")
	}
	h.svc.SetRefreshCookie(c, out.RefreshToken, out.RefreshExpiresAt)
	return c.Redirect(http.StatusFound,
		h.gc.FrontendBaseURL+"/auth/google/done#token="+url.QueryEscape(out.Token))
}

func (h *GoogleHandler) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", h.gc.ClientID)
	form.Set("client_secret", h.gc.ClientSecret)
	form.Set("redirect_uri", h.gc.RedirectURL)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		googleTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := h.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", errors.New("empty access_token")
	}
	return tok.AccessToken, nil
}

func (h *GoogleHandler) fetchEmail(ctx context.Context, accessToken string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoEndpoint, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := h.http.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("userinfo status %d", resp.StatusCode)
	}
	var info struct {
		Email         string `json:"email"`
		VerifiedEmail *bool  `json:"verified_email"`
		EmailVerified *bool  `json:"email_verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", false, err
	}
	verified := (info.VerifiedEmail != nil && *info.VerifiedEmail) ||
		(info.EmailVerified != nil && *info.EmailVerified)
	return strings.ToLower(strings.TrimSpace(info.Email)), verified, nil
}
