package invites

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/auth"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/email"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
)

var (
	ErrInviteNotFound  = errors.New("invite not found")
	ErrInviteExpired   = errors.New("invite expired or already accepted")
	ErrInvitePending   = errors.New("pending invite already exists for this email")
	ErrInvalidLevelIDs = errors.New("one or more level ids do not belong to this tenant")
	ErrNoDefaultLevel  = errors.New("tenant has no department level to assign")
)

type Invite struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Email      string
	UserType   string
	LevelIDs   []uuid.UUID
	ExpiresAt  time.Time
	InvitedBy  *uuid.UUID
	AcceptedAt *time.Time
	CreatedAt  time.Time
}

type IssueInput struct {
	TenantID  uuid.UUID
	Email     string
	UserType  string
	LevelIDs  []uuid.UUID
	InviterID uuid.UUID
	TTLHours  int
	BaseURL   string
}

type IssueOutput struct {
	Invite    Invite
	Token     string
	AcceptURL string
}

type AcceptInput struct {
	Token       string
	Password    string
	DisplayName string
}

type AcceptOutput struct {
	UserID   uuid.UUID
	TenantID uuid.UUID
	UserType string
	LevelIDs []uuid.UUID
}

type Service struct {
	pool   *pgxpool.Pool
	q      *sqlc.Queries
	sender email.Sender
}

func NewService(pool *pgxpool.Pool, q *sqlc.Queries, sender email.Sender) *Service {
	return &Service{pool: pool, q: q, sender: sender}
}

func newToken() (raw string, hashed []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	raw = hex.EncodeToString(buf)
	h := sha256.Sum256([]byte(raw))
	return raw, h[:], nil
}

// Issue creates a fresh invite and dispatches it via the configured sender.
// Email-send errors are logged but do not roll back the invite row; admins can
// call Resend to retry delivery.
func (s *Service) Issue(ctx context.Context, in IssueInput) (IssueOutput, error) {
	switch in.UserType {
	case "admin", "worker":
	default:
		return IssueOutput{}, fmt.Errorf("user_type must be admin or worker, got %q", in.UserType)
	}
	if in.UserType == "worker" && len(in.LevelIDs) == 0 {
		// Default a worker invite with no explicit levels to the tenant's
		// General/Employee level, so single-invite callers (and the SPA admin
		// UI) need no level-catalog knowledge. Bulk invites resolve this once
		// and pass it explicitly.
		lvlID, _, derr := s.resolveDefaultLevelID(ctx, in.TenantID)
		if derr != nil {
			return IssueOutput{}, derr
		}
		in.LevelIDs = []uuid.UUID{lvlID}
	}

	raw, hashed, err := newToken()
	if err != nil {
		return IssueOutput{}, err
	}
	expiresAt := time.Now().Add(time.Duration(in.TTLHours) * time.Hour)

	var inviter *uuid.UUID
	if in.InviterID != uuid.Nil {
		id := in.InviterID
		inviter = &id
	}

	// invites.level_ids is NOT NULL; a nil slice would encode as SQL NULL and
	// violate it. Admin invites legitimately carry no levels, so send an empty
	// (non-nil) array -> '{}'.
	levelIDs := in.LevelIDs
	if levelIDs == nil {
		levelIDs = []uuid.UUID{}
	}

	row, err := s.q.CreateInvite(ctx, sqlc.CreateInviteParams{
		TenantID:  in.TenantID,
		Email:     in.Email,
		UserType:  in.UserType,
		LevelIds:  levelIDs,
		TokenHash: hashed,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		InvitedBy: inviter,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23514": // check_violation, used by the level-id tenant trigger
				return IssueOutput{}, ErrInvalidLevelIDs
			case "23505": // unique_violation: pending invite for this email
				return IssueOutput{}, ErrInvitePending
			}
		}
		return IssueOutput{}, fmt.Errorf("create invite: %w", err)
	}

	var tenantName string
	if err := s.pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, in.TenantID).Scan(&tenantName); err != nil {
		return IssueOutput{}, fmt.Errorf("tenant name: %w", err)
	}
	var inviterName string
	if in.InviterID != uuid.Nil {
		_ = s.pool.QueryRow(ctx, `SELECT display_name FROM users WHERE id = $1`, in.InviterID).Scan(&inviterName)
	}

	acceptURL := strings.TrimRight(in.BaseURL, "/") + "/invite?token=" + raw
	_ = s.sender.SendInvite(ctx, email.InviteEmail{
		To: in.Email, TenantName: tenantName, InviterName: inviterName,
		AcceptURL: acceptURL, ExpiresAt: expiresAt,
	})

	return IssueOutput{
		Invite:    inviteFromCreateRow(row),
		Token:     raw,
		AcceptURL: acceptURL,
	}, nil
}

// resolveDefaultLevelID picks the level a worker invite defaults to when the
// caller does not specify level_ids: the lowest-rank level of the tenant's
// "General" department (Employee), falling back to the lowest-rank level of
// the first department if General is gone. ListLevelsForTenant orders by
// (department_name, rank), so the first matching row is the lowest rank.
// Returns ErrNoDefaultLevel when the tenant has no levels at all.
func (s *Service) resolveDefaultLevelID(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, string, error) {
	rows, err := s.q.ListLevelsForTenant(ctx, tenantID)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("list levels: %w", err)
	}
	if len(rows) == 0 {
		return uuid.Nil, "", ErrNoDefaultLevel
	}
	pick := 0
	for i, r := range rows {
		if r.DepartmentName == tenants.DefaultDepartmentName {
			pick = i
			break
		}
	}
	r := rows[pick]
	return r.ID, r.DepartmentName + " / " + r.Name, nil
}

// BulkIssueInput is the batch form of IssueInput: every email is invited as a
// worker at the tenant's default level.
type BulkIssueInput struct {
	TenantID  uuid.UUID
	Emails    []string
	InviterID uuid.UUID
	TTLHours  int
	BaseURL   string
}

type SkippedInvite struct {
	Email  string
	Reason string
}

type BulkInviteResult struct {
	LevelID   uuid.UUID
	LevelName string
	Invited   []string
	Skipped   []SkippedInvite
}

// IssueBulk invites every email as a worker at the tenant's default
// General/Employee level. It never aborts the batch: a per-email failure
// (already-pending invite, invalid level) is recorded in Skipped and the
// remaining emails still proceed.
func (s *Service) IssueBulk(ctx context.Context, in BulkIssueInput) (BulkInviteResult, error) {
	lvlID, lvlName, err := s.resolveDefaultLevelID(ctx, in.TenantID)
	if err != nil {
		return BulkInviteResult{}, err
	}
	res := BulkInviteResult{LevelID: lvlID, LevelName: lvlName}
	for _, e := range in.Emails {
		_, ierr := s.Issue(ctx, IssueInput{
			TenantID: in.TenantID, Email: e, UserType: "worker",
			LevelIDs: []uuid.UUID{lvlID}, InviterID: in.InviterID,
			TTLHours: in.TTLHours, BaseURL: in.BaseURL,
		})
		switch {
		case ierr == nil:
			res.Invited = append(res.Invited, e)
		case errors.Is(ierr, ErrInvitePending):
			res.Skipped = append(res.Skipped, SkippedInvite{Email: e, Reason: "invite already pending"})
		case errors.Is(ierr, ErrInvalidLevelIDs):
			res.Skipped = append(res.Skipped, SkippedInvite{Email: e, Reason: "invalid level"})
		default:
			res.Skipped = append(res.Skipped, SkippedInvite{Email: e, Reason: "invite failed"})
		}
	}
	return res, nil
}

// Accept consumes a token, creates the user, grants the level memberships
// attached to the invite, and marks the invite accepted. All in one transaction
// so a partial failure leaves the invite reusable.
func (s *Service) Accept(ctx context.Context, in AcceptInput) (AcceptOutput, error) {
	if in.Password == "" {
		return AcceptOutput{}, errors.New("password required")
	}
	h := sha256.Sum256([]byte(in.Token))

	row, err := s.q.GetInviteByTokenHash(ctx, h[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AcceptOutput{}, ErrInviteExpired
		}
		return AcceptOutput{}, fmt.Errorf("lookup invite: %w", err)
	}

	pwHash, err := auth.HashPassword(in.Password)
	if err != nil {
		return AcceptOutput{}, fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AcceptOutput{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	user, err := qtx.CreateUser(ctx, sqlc.CreateUserParams{
		TenantID:     row.TenantID,
		Email:        row.Email,
		PasswordHash: pwHash,
		DisplayName:  in.DisplayName,
		UserType:     row.UserType,
	})
	if err != nil {
		return AcceptOutput{}, fmt.Errorf("create user: %w", err)
	}
	for _, lvlID := range row.LevelIds {
		if err := qtx.GrantMembership(ctx, sqlc.GrantMembershipParams{
			UserID:            user.ID,
			DepartmentLevelID: lvlID,
			GrantedBy:         row.InvitedBy,
		}); err != nil {
			return AcceptOutput{}, fmt.Errorf("grant level: %w", err)
		}
	}

	if err := qtx.MarkInviteAccepted(ctx, row.ID); err != nil {
		return AcceptOutput{}, fmt.Errorf("mark accepted: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AcceptOutput{}, err
	}

	return AcceptOutput{
		UserID:   user.ID,
		TenantID: row.TenantID,
		UserType: row.UserType,
		LevelIDs: row.LevelIds,
	}, nil
}

func (s *Service) Revoke(ctx context.Context, tenantID, inviteID uuid.UUID) error {
	return s.q.DeleteInviteByID(ctx, sqlc.DeleteInviteByIDParams{ID: inviteID, TenantID: tenantID})
}

func (s *Service) Resend(ctx context.Context, tenantID, inviteID uuid.UUID, ttlHours int, baseURL string) (IssueOutput, error) {
	row, err := s.q.GetInviteByID(ctx, sqlc.GetInviteByIDParams{ID: inviteID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IssueOutput{}, ErrInviteNotFound
		}
		return IssueOutput{}, fmt.Errorf("lookup: %w", err)
	}
	if row.AcceptedAt.Valid {
		return IssueOutput{}, ErrInviteExpired
	}
	raw, hashed, err := newToken()
	if err != nil {
		return IssueOutput{}, err
	}
	expiresAt := time.Now().Add(time.Duration(ttlHours) * time.Hour)
	if err := s.q.ResetInviteToken(ctx, sqlc.ResetInviteTokenParams{
		ID:        inviteID,
		TokenHash: hashed,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		TenantID:  tenantID,
	}); err != nil {
		return IssueOutput{}, fmt.Errorf("reset token: %w", err)
	}

	var tenantName string
	_ = s.pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, tenantID).Scan(&tenantName)
	acceptURL := strings.TrimRight(baseURL, "/") + "/invite?token=" + raw
	_ = s.sender.SendInvite(ctx, email.InviteEmail{
		To: row.Email, TenantName: tenantName,
		AcceptURL: acceptURL, ExpiresAt: expiresAt,
	})

	inv := Invite{
		ID:        row.ID,
		TenantID:  row.TenantID,
		Email:     row.Email,
		UserType:  row.UserType,
		LevelIDs:  row.LevelIds,
		ExpiresAt: expiresAt,
		CreatedAt: row.CreatedAt.Time,
	}
	if row.InvitedBy != nil {
		u := *row.InvitedBy
		inv.InvitedBy = &u
	}
	return IssueOutput{Invite: inv, Token: raw, AcceptURL: acceptURL}, nil
}

func (s *Service) ListPending(ctx context.Context, tenantID uuid.UUID) ([]Invite, error) {
	rows, err := s.q.ListPendingInvitesByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]Invite, 0, len(rows))
	for _, r := range rows {
		inv := Invite{
			ID:        r.ID,
			TenantID:  r.TenantID,
			Email:     r.Email,
			UserType:  r.UserType,
			LevelIDs:  r.LevelIds,
			ExpiresAt: r.ExpiresAt.Time,
			CreatedAt: r.CreatedAt.Time,
		}
		if r.InvitedBy != nil {
			u := *r.InvitedBy
			inv.InvitedBy = &u
		}
		out = append(out, inv)
	}
	return out, nil
}

func inviteFromCreateRow(r sqlc.CreateInviteRow) Invite {
	inv := Invite{
		ID:        r.ID,
		TenantID:  r.TenantID,
		Email:     r.Email,
		UserType:  r.UserType,
		LevelIDs:  r.LevelIds,
		ExpiresAt: r.ExpiresAt.Time,
		CreatedAt: r.CreatedAt.Time,
	}
	if r.InvitedBy != nil {
		u := *r.InvitedBy
		inv.InvitedBy = &u
	}
	if r.AcceptedAt.Valid {
		at := r.AcceptedAt.Time
		inv.AcceptedAt = &at
	}
	return inv
}
