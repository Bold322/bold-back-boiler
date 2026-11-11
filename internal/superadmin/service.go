// Package superadmin holds the service and handler for the platform-wide
// admin tier. Superadmins create tenants, suspend/unsuspend, soft-delete,
// and restore them. They live in a synthetic platform tenant created by
// migration 20260516000001; their JWT carries user_type='superadmin' and
// an empty level_ids list (they hold no department memberships).
package superadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/admin"
	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
	"github.com/Bold322/bold-back-boiler/internal/invites"
	"github.com/Bold322/bold-back-boiler/internal/tenants"
)

var (
	ErrTenantNotFound = errors.New("tenant not found")
	// ErrAdminEmailRequired is returned when CreateTenant is called without
	// an initial admin email; a tenant with no way in is useless.
	ErrAdminEmailRequired = errors.New("initial_admin_email is required")
)

type Service struct {
	pool    *pgxpool.Pool
	q       *sqlc.Queries
	tenants *tenants.Service
	invites *invites.Service
	admin   *admin.Service
}

func NewService(pool *pgxpool.Pool, q *sqlc.Queries, tenantSvc *tenants.Service, inviteSvc *invites.Service) *Service {
	return &Service{pool: pool, q: q, tenants: tenantSvc, invites: inviteSvc}
}

// SetAdmin wires the per-tenant admin service used by cross-tenant superadmin
// member operations (list/delete/restore). The admin service is constructed
// elsewhere (main.go) so this setter avoids an import cycle.
func (s *Service) SetAdmin(adminSvc *admin.Service) { s.admin = adminSvc }

type TenantSummary struct {
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Status      string          `json:"status"`
	Tier        string          `json:"tier"`
	Flags       map[string]bool `json:"flags"`
	DeletedAt   *time.Time      `json:"deleted_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	MemberCount int             `json:"member_count"`
}

func (s *Service) ListTenants(ctx context.Context, includeDeleted bool) ([]TenantSummary, error) {
	rows, err := s.q.ListAllTenantsWithCounts(ctx, includeDeleted)
	if err != nil {
		return nil, err
	}
	out := make([]TenantSummary, 0, len(rows))
	for _, r := range rows {
		flags := map[string]bool{}
		if len(r.Flags) > 0 {
			_ = json.Unmarshal(r.Flags, &flags)
		}
		ts := TenantSummary{
			ID: r.ID, Name: r.Name, Slug: r.Slug, Status: r.Status,
			Tier: r.Tier, Flags: flags,
			CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
			MemberCount: int(r.MemberCount),
		}
		if r.DeletedAt.Valid {
			t := r.DeletedAt.Time
			ts.DeletedAt = &t
		}
		out = append(out, ts)
	}
	return out, nil
}

// CreateTenant provisions the tenant (row + General department + default
// levels) and then issues the bootstrap *admin* invite to InitialAdminEmail,
// emailing it via the configured sender (SES on, log otherwise). Existing
// users only / invite-only is preserved: this admin is the single way into a
// brand-new tenant. The tenant is committed before the invite is issued, so a
// rare invite failure leaves a tenant with no admin — the superadmin can
// delete the tenant and retry, or re-issue once an admin exists.
func (s *Service) CreateTenant(ctx context.Context, in tenants.CreateTenantInput) (tenants.CreateTenantOutput, error) {
	email := strings.ToLower(strings.TrimSpace(in.InitialAdminEmail))
	if email == "" {
		return tenants.CreateTenantOutput{}, ErrAdminEmailRequired
	}

	out, err := s.tenants.CreateTenant(ctx, in)
	if err != nil {
		return out, err
	}

	iss, err := s.invites.Issue(ctx, invites.IssueInput{
		TenantID:  out.Tenant.ID,
		Email:     email,
		UserType:  "admin",
		LevelIDs:  nil, // admins do not participate in document RBAC
		InviterID: uuid.Nil,
		TTLHours:  in.InviteTTLHours,
		BaseURL:   in.PublicBaseURL,
	})
	if err != nil {
		return out, fmt.Errorf("issue bootstrap admin invite: %w", err)
	}
	out.InviteID = iss.Invite.ID
	out.InviteToken = iss.Token
	out.AcceptURL = iss.AcceptURL
	out.ExpiresAt = iss.Invite.ExpiresAt
	return out, nil
}

func (s *Service) SuspendTenant(ctx context.Context, id uuid.UUID) error {
	return s.tenants.SuspendTenant(ctx, id)
}

func (s *Service) UnsuspendTenant(ctx context.Context, id uuid.UUID) error {
	return s.tenants.UnsuspendTenant(ctx, id)
}

func (s *Service) SoftDeleteTenant(ctx context.Context, id uuid.UUID) error {
	return s.tenants.SoftDeleteTenant(ctx, id)
}

func (s *Service) RestoreTenant(ctx context.Context, id uuid.UUID, graceHours int) error {
	return s.tenants.RestoreTenant(ctx, id, graceHours)
}

// requireExists is a defensive lookup for handlers that need to 404 before
// invoking a tenants-service method that would otherwise silently succeed
// on a missing row.
func (s *Service) requireExists(ctx context.Context, id uuid.UUID) error {
	_, err := s.q.GetTenantByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTenantNotFound
		}
		return fmt.Errorf("lookup tenant: %w", err)
	}
	return nil
}

// Cross-tenant member operations ----------------------------------------
//
// These wrap admin.Service / invites.Service with the path-supplied tenant
// id so a superadmin can manage any tenant's users without being logged into
// that tenant. The guards (last-admin, no superadmin delete, etc.) live in
// admin.Service and are reused as-is.

func (s *Service) ListUsersInTenant(ctx context.Context, tenantID uuid.UUID, q string, includeDeleted bool) ([]admin.UserRow, error) {
	if err := s.requireExists(ctx, tenantID); err != nil {
		return nil, err
	}
	return s.admin.ListUsers(ctx, tenantID, q, includeDeleted)
}

func (s *Service) InviteUserInTenant(ctx context.Context, tenantID uuid.UUID, in invites.IssueInput) (invites.IssueOutput, error) {
	if err := s.requireExists(ctx, tenantID); err != nil {
		return invites.IssueOutput{}, err
	}
	in.TenantID = tenantID
	return s.invites.Issue(ctx, in)
}

func (s *Service) RemoveUserFromTenant(ctx context.Context, tenantID, callerID, targetID uuid.UUID) error {
	if err := s.requireExists(ctx, tenantID); err != nil {
		return err
	}
	return s.admin.DeleteUser(ctx, tenantID, callerID, targetID)
}

func (s *Service) RestoreUserInTenant(ctx context.Context, tenantID, targetID uuid.UUID, graceHours int) error {
	if err := s.requireExists(ctx, tenantID); err != nil {
		return err
	}
	return s.admin.RestoreUser(ctx, tenantID, targetID, graceHours)
}
