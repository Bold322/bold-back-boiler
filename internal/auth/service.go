package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrPasswordRequired   = errors.New("current_password required to change password")
	ErrResetTokenInvalid  = errors.New("password reset token invalid or expired")
)

// PasswordResetTTL is how long a freshly issued reset link remains valid.
const PasswordResetTTL = 30 * time.Minute

type AuthOutput struct {
	Token            string
	RefreshToken     string
	RefreshExpiresAt time.Time
	User             PublicUser
}

type MembershipInfo struct {
	LevelID        uuid.UUID `json:"level_id"`
	LevelName      string    `json:"level_name"`
	LevelRank      int32     `json:"level_rank"`
	DepartmentID   uuid.UUID `json:"department_id"`
	DepartmentName string    `json:"department_name"`
}

type PublicUser struct {
	ID          uuid.UUID        `json:"id"`
	Email       string           `json:"email"`
	DisplayName string           `json:"display_name"`
	UserType    string           `json:"user_type"`
	Memberships []MembershipInfo `json:"memberships"`
	// Flags are the tenant's feature toggles (tenants.flags), surfaced so the
	// SPA can gate UI without a second round-trip.
	Flags map[string]bool `json:"flags"`
}

type Service struct {
	pool        *pgxpool.Pool
	q           *sqlc.Queries
	signer      *JWTSigner
	sender      email.Sender
	frontendURL string
	refresh     RefreshConfig
}

func NewService(pool *pgxpool.Pool, q *sqlc.Queries, signer *JWTSigner, sender email.Sender, frontendURL string, refresh RefreshConfig) *Service {
	return &Service{pool: pool, q: q, signer: signer, sender: sender, frontendURL: frontendURL, refresh: refresh}
}

// Login resolves email -> user globally (users.email is UNIQUE across all
// tenants), verifies the password, fetches the user's tenant to enforce
// active status, and issues a JWT carrying the user's tenant id, user_type,
// and level ids. Rejects login for tenants that are suspended or pending
// deletion. A missing user or wrong password both yield ErrInvalidCredentials
// so callers can't enumerate accounts.
func (s *Service) Login(ctx context.Context, email, password string) (*AuthOutput, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("login: get user by email failed", "err", err.Error())
		}
		return nil, ErrInvalidCredentials
	}
	if !VerifyPassword(row.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	tenant, err := s.q.GetTenantByID(ctx, row.TenantID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("login: tenant lookup failed", "err", err.Error())
		}
		return nil, ErrInvalidCredentials
	}
	if tenant.Status == "suspended" || tenant.DeletedAt.Valid {
		return nil, ErrInvalidCredentials
	}
	return s.issueToken(ctx, row.ID, tenant.ID, row.Email, row.DisplayName, row.UserType, tenant.Flags)
}

// LoginWithGoogle resolves a verified Google email -> existing user globally
// and issues a JWT, with no password check. It deliberately does NOT create
// users: Google sign-in authenticates only users who already exist (provisioned
// via admin invite), preserving the invite-only invariant. A missing user or
// inactive tenant returns ErrInvalidCredentials so the caller can surface a
// single "no account" outcome without leaking which part failed.
func (s *Service) LoginWithGoogle(ctx context.Context, email string) (*AuthOutput, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("google login: get user by email failed", "err", err.Error())
		}
		return nil, ErrInvalidCredentials
	}
	tenant, err := s.q.GetTenantByID(ctx, row.TenantID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("google login: tenant lookup failed", "err", err.Error())
		}
		return nil, ErrInvalidCredentials
	}
	if tenant.Status == "suspended" || tenant.DeletedAt.Valid {
		return nil, ErrInvalidCredentials
	}
	return s.issueToken(ctx, row.ID, tenant.ID, row.Email, row.DisplayName, row.UserType, tenant.Flags)
}

type UpdateMeInput struct {
	DisplayName     string
	NewPassword     string
	CurrentPassword string
}

// GetPublicUser loads a user and their department memberships, returning the
// public projection without the password hash.
func (s *Service) GetPublicUser(ctx context.Context, userID uuid.UUID) (PublicUser, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return PublicUser{}, err
	}
	memberships, err := s.loadMemberships(ctx, userID)
	if err != nil {
		return PublicUser{}, err
	}
	tenant, err := s.q.GetTenantByID(ctx, u.TenantID)
	if err != nil {
		return PublicUser{}, err
	}
	return PublicUser{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName,
		UserType: u.UserType, Memberships: memberships,
		Flags: parseFlags(tenant.Flags),
	}, nil
}

// UpdateMe lets a user change their own display name and/or password.
// Empty fields mean "no change". Changing the password requires
// CurrentPassword to verify ownership of the account. sessionID is the
// caller's own refresh-token family (from the access token's sid claim): a
// password change revokes every OTHER session for the user but leaves this
// one alive, since the request is already authenticated proof of possession
// of the current session.
func (s *Service) UpdateMe(ctx context.Context, userID, sessionID uuid.UUID, in UpdateMeInput) (PublicUser, error) {
	if in.NewPassword != "" {
		if in.CurrentPassword == "" {
			return PublicUser{}, ErrPasswordRequired
		}
		if len(in.NewPassword) < minPasswordLen {
			return PublicUser{}, ErrPasswordTooShort
		}
		full, err := s.q.GetUserByIDWithPassword(ctx, userID)
		if err != nil {
			return PublicUser{}, fmt.Errorf("load user: %w", err)
		}
		if !VerifyPassword(full.PasswordHash, in.CurrentPassword) {
			return PublicUser{}, ErrInvalidCredentials
		}
		hash, err := HashPassword(in.NewPassword)
		if err != nil {
			return PublicUser{}, fmt.Errorf("hash password: %w", err)
		}
		if err := s.q.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{
			ID: userID, PasswordHash: hash,
		}); err != nil {
			return PublicUser{}, fmt.Errorf("update password: %w", err)
		}
		if err := s.q.RevokeRefreshTokensForUserExceptFamily(ctx, sqlc.RevokeRefreshTokensForUserExceptFamilyParams{
			UserID: userID, FamilyID: sessionID,
		}); err != nil {
			return PublicUser{}, fmt.Errorf("revoke other sessions: %w", err)
		}
	}
	if dn := strings.TrimSpace(in.DisplayName); dn != "" {
		if err := s.q.UpdateUserDisplayName(ctx, sqlc.UpdateUserDisplayNameParams{
			ID: userID, DisplayName: dn,
		}); err != nil {
			return PublicUser{}, fmt.Errorf("update display name: %w", err)
		}
	}
	return s.GetPublicUser(ctx, userID)
}

func (s *Service) loadMemberships(ctx context.Context, userID uuid.UUID) ([]MembershipInfo, error) {
	rows, err := s.q.ListUserMemberships(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load memberships: %w", err)
	}
	out := make([]MembershipInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, MembershipInfo{
			LevelID: r.LevelID, LevelName: r.LevelName, LevelRank: r.LevelRank,
			DepartmentID: r.DepartmentID, DepartmentName: r.DepartmentName,
		})
	}
	return out, nil
}

// RequestPasswordReset always returns nil: regardless of whether the email
// matched a user, the API responds 200 OK so callers can't enumerate accounts.
// When the email matches, a single-use token is persisted (SHA-256 of the
// plaintext) and an email goes out with the recovery link. Email-send
// failures are logged but not surfaced.
func (s *Service) RequestPasswordReset(ctx context.Context, emailAddr string) error {
	emailAddr = strings.TrimSpace(strings.ToLower(emailAddr))
	if emailAddr == "" {
		return nil
	}
	row, err := s.q.GetUserByEmail(ctx, emailAddr)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("forgot-password: user lookup failed", "err", err.Error())
		}
		return nil
	}
	raw, hashed, err := newResetToken()
	if err != nil {
		return fmt.Errorf("generate reset token: %w", err)
	}
	expiresAt := time.Now().Add(PasswordResetTTL)
	if err := s.q.InsertPasswordResetToken(ctx, sqlc.InsertPasswordResetTokenParams{
		UserID: row.ID, TokenHash: hashed,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return fmt.Errorf("insert reset token: %w", err)
	}
	resetURL := strings.TrimRight(s.frontendURL, "/") + "/reset-password?token=" + raw
	if err := s.sender.SendPasswordReset(ctx, email.PasswordResetEmail{
		To: row.Email, ResetURL: resetURL, ExpiresAt: expiresAt,
	}); err != nil {
		slog.Warn("forgot-password: send failed", "user_id", row.ID.String(), "err", err.Error())
	}
	return nil
}

// ResetPassword consumes a reset token and sets the user's password. The
// token is single-use: marking it used + invalidating any sibling tokens
// happens in the same transaction as the password update so a replay or
// race can't slip a second reset through.
func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	if rawToken == "" {
		return ErrResetTokenInvalid
	}
	if len(newPassword) < minPasswordLen {
		return ErrPasswordTooShort
	}
	hashed := sha256.Sum256([]byte(rawToken))
	row, err := s.q.GetPasswordResetTokenByHash(ctx, hashed[:])
	if err != nil {
		return ErrResetTokenInvalid
	}
	if row.UsedAt.Valid || row.ExpiresAt.Time.Before(time.Now()) {
		return ErrResetTokenInvalid
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	rows, err := qtx.MarkPasswordResetTokenUsed(ctx, row.ID)
	if err != nil {
		return fmt.Errorf("mark token used: %w", err)
	}
	if rows == 0 {
		return ErrResetTokenInvalid
	}
	if err := qtx.UpdateUserPassword(ctx, sqlc.UpdateUserPasswordParams{
		ID: row.UserID, PasswordHash: hash,
	}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if err := qtx.InvalidatePasswordResetTokensForUser(ctx, row.UserID); err != nil {
		return fmt.Errorf("invalidate sibling tokens: %w", err)
	}
	if err := qtx.RevokeRefreshTokensForUser(ctx, row.UserID); err != nil {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func newResetToken() (raw string, hashed []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	raw = hex.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

func (s *Service) issueToken(ctx context.Context, userID, tenantID uuid.UUID, email, displayName, userType string, flags []byte) (*AuthOutput, error) {
	levelIDs, err := s.q.GetUserLevelIDs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load level ids: %w", err)
	}
	memberships, err := s.loadMemberships(ctx, userID)
	if err != nil {
		return nil, err
	}
	sess, err := s.MintSession(ctx, userID, tenantID, userType, levelIDs)
	if err != nil {
		return nil, fmt.Errorf("mint session: %w", err)
	}
	return &AuthOutput{
		Token:            sess.AccessToken,
		RefreshToken:     sess.RefreshToken,
		RefreshExpiresAt: sess.RefreshExpiresAt,
		User: PublicUser{
			ID: userID, Email: email, DisplayName: displayName,
			UserType: userType, Memberships: memberships,
			Flags: parseFlags(flags),
		},
	}, nil
}

// parseFlags decodes the tenants.flags jsonb column into a flat bool map.
// Malformed or empty input yields an empty (non-nil) map so JSON renders {}.
func parseFlags(raw []byte) map[string]bool {
	out := map[string]bool{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}
