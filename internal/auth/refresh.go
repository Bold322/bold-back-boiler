package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

var (
	ErrRefreshInvalid = errors.New("refresh token invalid or expired")
	ErrRefreshReuse   = errors.New("refresh token reuse detected")
	ErrRefreshMissing = errors.New("refresh token missing")
)

// RefreshConfig carries the resolved refresh-token and cookie settings.
type RefreshConfig struct {
	CookieName   string
	CookieDomain string
	SameSite     http.SameSite
	Secure       bool
	IdleTTL      time.Duration
	AbsoluteTTL  time.Duration
}

// RefreshOutput is the token pair produced by MintSession and Refresh.
type RefreshOutput struct {
	AccessToken      string
	RefreshToken     string
	RefreshExpiresAt time.Time
}

func newRefreshToken() (raw string, hashed []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

func hashRefreshToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// MintSession opens a new refresh-token family for a freshly authenticated
// user and signs an access token bound to that family via the sid claim.
func (s *Service) MintSession(ctx context.Context, userID, tenantID uuid.UUID, userType string, levelIDs []uuid.UUID) (RefreshOutput, error) {
	familyID := uuid.New()
	now := time.Now()
	expiresAt := now.Add(s.refresh.IdleTTL)
	raw, hashed, err := newRefreshToken()
	if err != nil {
		return RefreshOutput{}, err
	}
	if err := s.q.InsertRefreshToken(ctx, sqlc.InsertRefreshTokenParams{
		TenantID:        tenantID,
		UserID:          userID,
		FamilyID:        familyID,
		TokenHash:       hashed,
		ExpiresAt:       pgtype.Timestamptz{Time: expiresAt, Valid: true},
		FamilyStartedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		return RefreshOutput{}, fmt.Errorf("insert refresh token: %w", err)
	}
	access, err := s.signer.SignWithSession(userID, tenantID, userType, levelIDs, familyID)
	if err != nil {
		return RefreshOutput{}, fmt.Errorf("sign access token: %w", err)
	}
	return RefreshOutput{AccessToken: access, RefreshToken: raw, RefreshExpiresAt: expiresAt}, nil
}

// Refresh validates and rotates a refresh token, returning a fresh access
// token (with reloaded claims) and a new refresh token. Presenting an
// already-rotated or revoked token revokes the whole family (ErrRefreshReuse).
func (s *Service) Refresh(ctx context.Context, rawToken string) (RefreshOutput, error) {
	if rawToken == "" {
		return RefreshOutput{}, ErrRefreshMissing
	}
	row, err := s.q.GetRefreshTokenByHash(ctx, hashRefreshToken(rawToken))
	if err != nil {
		return RefreshOutput{}, ErrRefreshInvalid
	}
	if row.RotatedAt.Valid || row.RevokedAt.Valid {
		_ = s.q.RevokeRefreshFamily(ctx, row.FamilyID)
		return RefreshOutput{}, ErrRefreshReuse
	}
	now := time.Now()
	if !row.ExpiresAt.Valid || row.ExpiresAt.Time.Before(now) {
		return RefreshOutput{}, ErrRefreshInvalid
	}
	absoluteDeadline := row.FamilyStartedAt.Time.Add(s.refresh.AbsoluteTTL)
	if !now.Before(absoluteDeadline) {
		_ = s.q.RevokeRefreshFamily(ctx, row.FamilyID)
		return RefreshOutput{}, ErrRefreshInvalid
	}
	user, err := s.q.GetUserByID(ctx, row.UserID)
	if err != nil || user.DeletedAt.Valid {
		_ = s.q.RevokeRefreshFamily(ctx, row.FamilyID)
		return RefreshOutput{}, ErrRefreshInvalid
	}
	tenant, err := s.q.GetTenantByID(ctx, row.TenantID)
	if err != nil || tenant.Status == "suspended" || tenant.DeletedAt.Valid {
		_ = s.q.RevokeRefreshFamily(ctx, row.FamilyID)
		return RefreshOutput{}, ErrRefreshInvalid
	}
	levelIDs, err := s.q.GetUserLevelIDs(ctx, row.UserID)
	if err != nil {
		return RefreshOutput{}, fmt.Errorf("load level ids: %w", err)
	}
	newExpires := now.Add(s.refresh.IdleTTL)
	if newExpires.After(absoluteDeadline) {
		newExpires = absoluteDeadline
	}
	raw, hashed, err := newRefreshToken()
	if err != nil {
		return RefreshOutput{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RefreshOutput{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	rotated, err := qtx.MarkRefreshTokenRotated(ctx, row.ID)
	if err != nil {
		return RefreshOutput{}, fmt.Errorf("rotate: %w", err)
	}
	if rotated == 0 {
		// Lost the rotation race: a concurrent refresh already consumed this
		// token. Treat as reuse and kill the family.
		_ = s.q.RevokeRefreshFamily(ctx, row.FamilyID)
		return RefreshOutput{}, ErrRefreshReuse
	}
	if err := qtx.InsertRefreshToken(ctx, sqlc.InsertRefreshTokenParams{
		TenantID:        row.TenantID,
		UserID:          row.UserID,
		FamilyID:        row.FamilyID,
		TokenHash:       hashed,
		ExpiresAt:       pgtype.Timestamptz{Time: newExpires, Valid: true},
		FamilyStartedAt: row.FamilyStartedAt,
	}); err != nil {
		return RefreshOutput{}, fmt.Errorf("insert rotated token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RefreshOutput{}, fmt.Errorf("commit: %w", err)
	}
	access, err := s.signer.SignWithSession(row.UserID, row.TenantID, user.UserType, levelIDs, row.FamilyID)
	if err != nil {
		return RefreshOutput{}, fmt.Errorf("sign access token: %w", err)
	}
	return RefreshOutput{AccessToken: access, RefreshToken: raw, RefreshExpiresAt: newExpires}, nil
}

// Logout revokes the family of the presented refresh token. Idempotent: an
// unknown or empty token is a no-op.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	row, err := s.q.GetRefreshTokenByHash(ctx, hashRefreshToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	return s.q.RevokeRefreshFamily(ctx, row.FamilyID)
}

func (s *Service) SetRefreshCookie(c echo.Context, raw string, expires time.Time) {
	c.SetCookie(&http.Cookie{
		Name:     s.refresh.CookieName,
		Value:    raw,
		Path:     "/auth",
		Domain:   s.refresh.CookieDomain,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   s.refresh.Secure,
		SameSite: s.refresh.SameSite,
	})
}

func (s *Service) ClearRefreshCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     s.refresh.CookieName,
		Value:    "",
		Path:     "/auth",
		Domain:   s.refresh.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.refresh.Secure,
		SameSite: s.refresh.SameSite,
	})
}

func (s *Service) RefreshCookieName() string { return s.refresh.CookieName }
