package invites_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
	"github.com/Bold322/bold-back-boiler/internal/invites"
)

func skipIfNoDB(t *testing.T) {
	if os.Getenv("RUN_DB_TESTS") == "" {
		t.Skip("set RUN_DB_TESTS=1 to run DB integration tests")
	}
}

// newTestAcceptHandlerWithInvite seeds a tenant, issues a worker invite, and
// wires an AcceptHandler over a real invites.Service + auth.Service (with a
// working refresh-cookie config). Returns the handler and the invite's raw
// token, ready to be accepted by a test.
func newTestAcceptHandlerWithInvite(t *testing.T) (h *invites.AcceptHandler, rawToken string) {
	t.Helper()
	pool := newPool(t)

	inviteSvc := invites.NewService(pool, sqlc.New(pool), email.NoopSender{})
	tid, workerLevelID, inviter := seedTenant(t, pool)

	out, err := inviteSvc.Issue(context.Background(), invites.IssueInput{
		TenantID: tid, Email: "accept-" + strings.ToLower(uuid.New().String()[:8]) + "@x.com",
		UserType: "worker",
		LevelIDs: []uuid.UUID{workerLevelID}, InviterID: inviter,
		TTLHours: 168, BaseURL: "https://app.example.com",
	})
	require.NoError(t, err)

	signer := auth.NewJWTSigner("test-secret-at-least-32-chars-long!!", "test-issuer", 900)
	authSvc := auth.NewService(pool, sqlc.New(pool), signer, email.NoopSender{}, "http://localhost:4200", auth.RefreshConfig{
		CookieName:  "app_refresh",
		IdleTTL:     14 * 24 * time.Hour,
		AbsoluteTTL: 90 * 24 * time.Hour,
	})

	return invites.NewAcceptHandler(inviteSvc, authSvc), out.Token
}

func TestAcceptInviteSetsRefreshCookie(t *testing.T) {
	skipIfNoDB(t)
	h, rawToken := newTestAcceptHandlerWithInvite(t) // seeds tenant + pending invite

	e := echo.New()
	body := `{"token":"` + rawToken + `","password":"hunter2hunter2","display_name":"New User"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/accept-invite", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Accept(c))
	require.Equal(t, http.StatusCreated, rec.Code)

	var found bool
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "app_refresh" && ck.Value != "" {
			found = true
		}
	}
	require.True(t, found, "expected refresh cookie on accept-invite")
}
