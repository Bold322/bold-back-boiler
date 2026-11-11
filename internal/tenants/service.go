package tenants

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

var (
	ErrSlugInvalid     = errors.New("tenant slug must match ^[a-z0-9][a-z0-9-]{1,62}$")
	ErrSlugTaken       = errors.New("tenant slug already exists")
	ErrTenantNotFound  = errors.New("tenant not found")
	ErrTenantSuspended = errors.New("tenant is suspended")
	ErrTenantDeleted   = errors.New("tenant has been deleted")
	ErrRestoreExpired  = errors.New("restore window has expired")
)

var slugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

type Tenant struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	Status    string
	DeletedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateTenantInput struct {
	Name              string
	Slug              string
	InitialAdminEmail string
	InviteTTLHours    int
	PublicBaseURL     string
}

type CreateTenantOutput struct {
	Tenant      Tenant
	InviteID    uuid.UUID
	InviteToken string
	AcceptURL   string
	ExpiresAt   time.Time
}

type Service struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

func NewService(pool *pgxpool.Pool, q *sqlc.Queries) *Service {
	return &Service{pool: pool, q: q}
}

// CreateTenant inserts the tenant row, seeds the General department with four
// default levels, and returns. It deliberately issues no bootstrap admin
// invite (that would couple tenants to invites): the orchestrating callers,
// superadmin.Service.CreateTenant and cmd/seed, wrap this call and fill
// InviteToken, AcceptURL, and ExpiresAt in the output. From this function
// alone those fields are zero/empty.
func (s *Service) CreateTenant(ctx context.Context, in CreateTenantInput) (CreateTenantOutput, error) {
	if !slugRegex.MatchString(in.Slug) {
		return CreateTenantOutput{}, ErrSlugInvalid
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreateTenantOutput{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	trow, err := qtx.CreateTenant(ctx, sqlc.CreateTenantParams{Name: in.Name, Slug: in.Slug})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return CreateTenantOutput{}, ErrSlugTaken
		}
		return CreateTenantOutput{}, fmt.Errorf("create tenant: %w", err)
	}

	// Seed the General department.
	dept, err := qtx.CreateDepartment(ctx, sqlc.CreateDepartmentParams{
		TenantID:    trow.ID,
		Name:        DefaultDepartmentName,
		Description: "",
	})
	if err != nil {
		return CreateTenantOutput{}, fmt.Errorf("seed department: %w", err)
	}

	// Seed levels and capture the bootstrap level id.
	var bootstrapLevelID uuid.UUID
	for _, lvl := range DefaultLevels {
		row, err := qtx.CreateDepartmentLevel(ctx, sqlc.CreateDepartmentLevelParams{
			DepartmentID: dept.ID,
			Name:         lvl.Name,
			Rank:         lvl.Rank,
		})
		if err != nil {
			return CreateTenantOutput{}, fmt.Errorf("seed level %s: %w", lvl.Name, err)
		}
		if lvl.Name == BootstrapAdminLevelName {
			bootstrapLevelID = row.ID
		}
	}
	if bootstrapLevelID == uuid.Nil {
		return CreateTenantOutput{}, fmt.Errorf("bootstrap level %q missing from DefaultLevels", BootstrapAdminLevelName)
	}

	// The bootstrap admin invite is issued by the orchestrating caller
	// (superadmin.Service.CreateTenant, cmd/seed), never here: tenants must
	// not depend on invites. bootstrapLevelID is resolved only to assert that
	// DefaultLevels still contains the bootstrap admin level name.
	_ = bootstrapLevelID

	if err := tx.Commit(ctx); err != nil {
		return CreateTenantOutput{}, fmt.Errorf("commit: %w", err)
	}

	return CreateTenantOutput{
		Tenant: tenantFromRow(trow),
	}, nil
}

func (s *Service) SuspendTenant(ctx context.Context, id uuid.UUID) error {
	return s.q.SuspendTenant(ctx, id)
}

func (s *Service) UnsuspendTenant(ctx context.Context, id uuid.UUID) error {
	return s.q.UnsuspendTenant(ctx, id)
}

func (s *Service) SoftDeleteTenant(ctx context.Context, id uuid.UUID) error {
	return s.q.SoftDeleteTenant(ctx, id)
}

func (s *Service) RestoreTenant(ctx context.Context, id uuid.UUID, graceHours int) error {
	_, err := s.q.RestoreTenant(ctx, sqlc.RestoreTenantParams{ID: id, Column2: int32(graceHours)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRestoreExpired
		}
		return err
	}
	return nil
}

// HardDeleteTenant deletes the tenants row; every tenant-scoped table
// cascades via FK. Hook external cleanup here (object storage prefixes,
// third-party accounts, ...) when your domain adds such resources.
func (s *Service) HardDeleteTenant(ctx context.Context, id uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete tenants row: %w", err)
	}
	return nil
}

func tenantFromRow(t sqlc.Tenant) Tenant {
	var deletedAt *time.Time
	if t.DeletedAt.Valid {
		dt := t.DeletedAt.Time
		deletedAt = &dt
	}
	return Tenant{
		ID:        t.ID,
		Name:      t.Name,
		Slug:      t.Slug,
		Status:    t.Status,
		DeletedAt: deletedAt,
		CreatedAt: t.CreatedAt.Time,
		UpdatedAt: t.UpdatedAt.Time,
	}
}
