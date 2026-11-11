package admin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Bold322/bold-back-boiler/internal/db/sqlc"
)

var (
	ErrLastAdmin               = errors.New("cannot demote the last admin in this tenant")
	ErrDepartmentAlreadyExists = errors.New("department already exists")
	ErrDepartmentNotFound      = errors.New("department not found")
	ErrDepartmentInUse         = errors.New("department is still in use")
	ErrInvalidDepartmentName   = errors.New("department name must be non-empty and at most 64 characters")
	ErrLevelNotFound           = errors.New("level not found")
	ErrLevelAlreadyExists      = errors.New("level with this name or rank already exists")
	ErrLevelInUse              = errors.New("level is still in use")
	ErrInvalidLevelName        = errors.New("level name must be non-empty and at most 64 characters")
	ErrInvalidRank             = errors.New("rank must be a positive integer")
	ErrUserNotFound            = errors.New("user not found")
	ErrCannotDeleteSelf        = errors.New("cannot delete your own user")
	ErrCannotDeleteSuperadmin  = errors.New("cannot delete a superadmin via the tenant admin route")
	ErrCannotDeleteLastAdmin   = errors.New("cannot delete the last admin in this tenant")
	ErrUserAlreadyDeleted      = errors.New("user is already deleted")
	ErrUserNotDeleted          = errors.New("user is not deleted")
	ErrRestoreExpired          = errors.New("restore grace window has expired")
)

var deptNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$`)

type Department struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	DeprecatedAt *time.Time `json:"deprecated_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Levels       []Level    `json:"levels,omitempty"`
}

type Level struct {
	ID           uuid.UUID `json:"id"`
	DepartmentID uuid.UUID `json:"department_id"`
	Name         string    `json:"name"`
	Rank         int32     `json:"rank"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type DepartmentInUseError struct {
	MembershipCount int64
}

func (e *DepartmentInUseError) Error() string { return ErrDepartmentInUse.Error() }
func (e *DepartmentInUseError) Unwrap() error { return ErrDepartmentInUse }

type LevelInUseError struct {
	MembershipCount int64
}

func (e *LevelInUseError) Error() string { return ErrLevelInUse.Error() }
func (e *LevelInUseError) Unwrap() error { return ErrLevelInUse }

type UserRow struct {
	ID          uuid.UUID   `json:"id"`
	Email       string      `json:"email"`
	DisplayName string      `json:"display_name"`
	UserType    string      `json:"user_type"`
	CreatedAt   time.Time   `json:"created_at"`
	DeletedAt   *time.Time  `json:"deleted_at,omitempty"`
	Levels      []string    `json:"levels"`
	LevelIDs    []uuid.UUID `json:"level_ids"`
}

type AuditEntry struct {
	ID           uuid.UUID `json:"id"`
	ActorUserID  uuid.UUID `json:"actor_user_id"`
	TargetUserID uuid.UUID `json:"target_user_id"`
	LevelID      uuid.UUID `json:"level_id"`
	LevelName    string    `json:"level_name"`
	Action       string    `json:"action"`
	At           time.Time `json:"at"`
}

type UpdateLevelInput struct {
	Name *string
	Rank *int32
}

type Service struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
}

func NewService(pool *pgxpool.Pool, q *sqlc.Queries) *Service {
	return &Service{pool: pool, q: q}
}

// Users -----------------------------------------------------------------

func (s *Service) ListUsers(ctx context.Context, tenantID uuid.UUID, q string, includeDeleted bool) ([]UserRow, error) {
	rows, err := s.q.ListUsersInTenant(ctx, sqlc.ListUsersInTenantParams{
		TenantID: tenantID, IncludeDeleted: includeDeleted, Q: q,
	})
	if err != nil {
		return nil, err
	}
	out := make([]UserRow, 0, len(rows))
	for _, r := range rows {
		u := UserRow{
			ID: r.ID, Email: r.Email, DisplayName: r.DisplayName,
			UserType: r.UserType, CreatedAt: r.CreatedAt.Time,
			LevelIDs: r.LevelIds, Levels: r.LevelLabels,
		}
		if r.DeletedAt.Valid {
			t := r.DeletedAt.Time
			u.DeletedAt = &t
		}
		out = append(out, u)
	}
	return out, nil
}

func (s *Service) PromoteToAdmin(ctx context.Context, tenantID, actor, targetUserID uuid.UUID) error {
	return s.changeUserType(ctx, tenantID, targetUserID, "admin")
}

func (s *Service) DemoteToWorker(ctx context.Context, tenantID, actor, targetUserID uuid.UUID) error {
	count, err := s.q.CountAdminsInTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	target, err := s.q.GetUserByID(ctx, targetUserID)
	if err != nil {
		return err
	}
	if target.UserType == "admin" && count <= 1 {
		return ErrLastAdmin
	}
	return s.changeUserType(ctx, tenantID, targetUserID, "worker")
}

func (s *Service) changeUserType(ctx context.Context, tenantID, targetUserID uuid.UUID, newType string) error {
	target, err := s.q.GetUserByID(ctx, targetUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if target.TenantID != tenantID || target.DeletedAt.Valid {
		return ErrUserNotFound
	}
	if target.UserType == "superadmin" {
		return ErrCannotDeleteSuperadmin
	}
	return s.q.UpdateUserType(ctx, sqlc.UpdateUserTypeParams{ID: targetUserID, UserType: newType})
}

// DeleteUser soft-deletes a user in the caller's tenant. Guards: no self-delete,
// no deleting the last admin, no deleting superadmins via the tenant route. The
// row is preserved (deleted_at timestamp), so every uploaded_by / audit log FK
// stays valid. Login refuses the user because GetUserByEmail filters out
// deleted_at IS NOT NULL.
func (s *Service) DeleteUser(ctx context.Context, tenantID, callerID, targetID uuid.UUID) error {
	if callerID == targetID {
		return ErrCannotDeleteSelf
	}
	target, err := s.q.GetUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if target.TenantID != tenantID {
		return ErrUserNotFound
	}
	if target.DeletedAt.Valid {
		return ErrUserAlreadyDeleted
	}
	if target.UserType == "superadmin" {
		return ErrCannotDeleteSuperadmin
	}
	if target.UserType == "admin" {
		count, err := s.q.CountAdminsInTenant(ctx, tenantID)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrCannotDeleteLastAdmin
		}
	}
	_, err = s.q.SoftDeleteUserInTenant(ctx, sqlc.SoftDeleteUserInTenantParams{
		ID: targetID, TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Race: someone else deleted between our load and write.
			return ErrUserAlreadyDeleted
		}
		return err
	}
	if err := s.q.RevokeRefreshTokensForUser(ctx, targetID); err != nil {
		return fmt.Errorf("revoke refresh tokens: %w", err)
	}
	return nil
}

// RestoreUser undoes a soft-delete within the grace window. Outside the window
// the row is considered permanently deleted (a future sweeper will hard-delete).
// Returns ErrRestoreExpired so the handler can map to 410.
func (s *Service) RestoreUser(ctx context.Context, tenantID, targetID uuid.UUID, graceHours int) error {
	target, err := s.q.GetUserByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if target.TenantID != tenantID {
		return ErrUserNotFound
	}
	if !target.DeletedAt.Valid {
		return ErrUserNotDeleted
	}
	if graceHours <= 0 {
		graceHours = 168
	}
	_, err = s.q.RestoreUserInTenant(ctx, sqlc.RestoreUserInTenantParams{
		ID: targetID, TenantID: tenantID, Column3: int32(graceHours),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRestoreExpired
		}
		return err
	}
	return nil
}

// Memberships -----------------------------------------------------------

func (s *Service) GrantMembership(ctx context.Context, tenantID, actor, targetUserID, levelID uuid.UUID) error {
	lvl, err := s.q.GetDepartmentLevelByID(ctx, levelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLevelNotFound
		}
		return err
	}
	dept, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: lvl.DepartmentID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDepartmentNotFound
		}
		return err
	}
	if dept.DeprecatedAt.Valid {
		return fmt.Errorf("department %q is deprecated", dept.Name)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	actorPtr := actor
	if err := qtx.GrantMembership(ctx, sqlc.GrantMembershipParams{
		UserID: targetUserID, DepartmentLevelID: levelID, GrantedBy: &actorPtr,
	}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return fmt.Errorf("cross-tenant grant denied")
		}
		return err
	}
	label := dept.Name + " / " + lvl.Name
	if err := qtx.InsertMembershipAuditEntry(ctx, sqlc.InsertMembershipAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor, TargetUserID: targetUserID,
		LevelID: levelID, LevelName: label, Action: "grant",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) RevokeMembership(ctx context.Context, tenantID, actor, targetUserID, levelID uuid.UUID) error {
	lvl, err := s.q.GetDepartmentLevelByID(ctx, levelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLevelNotFound
		}
		return err
	}
	dept, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: lvl.DepartmentID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDepartmentNotFound
		}
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if _, err := qtx.RevokeMembership(ctx, sqlc.RevokeMembershipParams{
		UserID: targetUserID, DepartmentLevelID: levelID,
	}); err != nil {
		return err
	}
	label := dept.Name + " / " + lvl.Name
	if err := qtx.InsertMembershipAuditEntry(ctx, sqlc.InsertMembershipAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor, TargetUserID: targetUserID,
		LevelID: levelID, LevelName: label, Action: "revoke",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Audit -----------------------------------------------------------------

func (s *Service) ListAuditEntries(ctx context.Context, tenantID uuid.UUID, limit int32) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.q.ListMembershipAuditEntries(ctx, sqlc.ListMembershipAuditEntriesParams{TenantID: tenantID, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuditEntry{
			ID: r.ID, ActorUserID: r.ActorUserID, TargetUserID: r.TargetUserID,
			LevelID: r.LevelID, LevelName: r.LevelName, Action: r.Action, At: r.At.Time,
		})
	}
	return out, nil
}

// Departments ------------------------------------------------------------

func (s *Service) CreateDepartment(ctx context.Context, tenantID, actor uuid.UUID, name, description string) (Department, error) {
	if !deptNameRe.MatchString(name) {
		return Department{}, ErrInvalidDepartmentName
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Department{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.CreateDepartment(ctx, sqlc.CreateDepartmentParams{
		TenantID: tenantID, Name: name, Description: description,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Department{}, ErrDepartmentAlreadyExists
		}
		return Department{}, fmt.Errorf("create department: %w", err)
	}
	if err := qtx.InsertDepartmentCatalogAuditEntry(ctx, sqlc.InsertDepartmentCatalogAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor,
		DepartmentID: row.ID, DepartmentName: row.Name, Action: "create",
	}); err != nil {
		return Department{}, fmt.Errorf("audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Department{}, err
	}
	return departmentFromRow(row), nil
}

func (s *Service) GetDepartment(ctx context.Context, tenantID, id uuid.UUID) (Department, error) {
	row, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: id, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Department{}, ErrDepartmentNotFound
		}
		return Department{}, err
	}
	return departmentFromRow(row), nil
}

func (s *Service) ListDepartments(ctx context.Context, tenantID uuid.UUID, includeDeprecated bool) ([]Department, error) {
	rows, err := s.q.ListDepartmentsByTenant(ctx, sqlc.ListDepartmentsByTenantParams{
		TenantID: tenantID, Column2: includeDeprecated,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Department, 0, len(rows))
	for _, r := range rows {
		out = append(out, departmentFromRow(r))
	}
	return out, nil
}

func (s *Service) UpdateDepartmentDescription(ctx context.Context, tenantID, actor, id uuid.UUID, description string) (Department, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Department{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.UpdateDepartmentDescription(ctx, sqlc.UpdateDepartmentDescriptionParams{
		ID: id, TenantID: tenantID, Description: description,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Department{}, ErrDepartmentNotFound
		}
		return Department{}, err
	}
	if err := qtx.InsertDepartmentCatalogAuditEntry(ctx, sqlc.InsertDepartmentCatalogAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor,
		DepartmentID: row.ID, DepartmentName: row.Name, Action: "update",
	}); err != nil {
		return Department{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Department{}, err
	}
	return departmentFromRow(row), nil
}

func (s *Service) DeprecateDepartment(ctx context.Context, tenantID, actor, id uuid.UUID) (Department, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Department{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.DeprecateDepartment(ctx, sqlc.DeprecateDepartmentParams{ID: id, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Department{}, ErrDepartmentNotFound
		}
		return Department{}, err
	}
	if err := qtx.InsertDepartmentCatalogAuditEntry(ctx, sqlc.InsertDepartmentCatalogAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor,
		DepartmentID: row.ID, DepartmentName: row.Name, Action: "deprecate",
	}); err != nil {
		return Department{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Department{}, err
	}
	return departmentFromRow(row), nil
}

func (s *Service) UndeprecateDepartment(ctx context.Context, tenantID, actor, id uuid.UUID) (Department, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Department{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.UndeprecateDepartment(ctx, sqlc.UndeprecateDepartmentParams{ID: id, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Department{}, ErrDepartmentNotFound
		}
		return Department{}, err
	}
	if err := qtx.InsertDepartmentCatalogAuditEntry(ctx, sqlc.InsertDepartmentCatalogAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor,
		DepartmentID: row.ID, DepartmentName: row.Name, Action: "undeprecate",
	}); err != nil {
		return Department{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Department{}, err
	}
	return departmentFromRow(row), nil
}

func (s *Service) DeleteDepartment(ctx context.Context, tenantID, actor, id uuid.UUID) error {
	existing, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: id, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDepartmentNotFound
		}
		return err
	}
	counts, err := s.q.CountDepartmentReferences(ctx, id)
	if err != nil {
		return err
	}
	if counts > 0 {
		return &DepartmentInUseError{MembershipCount: counts}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.DeleteDepartment(ctx, sqlc.DeleteDepartmentParams{ID: id, TenantID: tenantID}); err != nil {
		return err
	}
	if err := qtx.InsertDepartmentCatalogAuditEntry(ctx, sqlc.InsertDepartmentCatalogAuditEntryParams{
		TenantID: tenantID, ActorUserID: actor,
		DepartmentID: existing.ID, DepartmentName: existing.Name, Action: "delete",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Levels ----------------------------------------------------------------

func (s *Service) CreateLevel(ctx context.Context, tenantID, actor, departmentID uuid.UUID, name string, rank int32) (Level, error) {
	if name == "" || len(name) > 64 {
		return Level{}, ErrInvalidLevelName
	}
	if rank <= 0 {
		return Level{}, ErrInvalidRank
	}
	if _, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: departmentID, TenantID: tenantID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Level{}, ErrDepartmentNotFound
		}
		return Level{}, err
	}
	row, err := s.q.CreateDepartmentLevel(ctx, sqlc.CreateDepartmentLevelParams{
		DepartmentID: departmentID, Name: name, Rank: rank,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Level{}, ErrLevelAlreadyExists
		}
		return Level{}, err
	}
	return levelFromRow(row), nil
}

func (s *Service) ListLevels(ctx context.Context, tenantID, departmentID uuid.UUID) ([]Level, error) {
	if _, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: departmentID, TenantID: tenantID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDepartmentNotFound
		}
		return nil, err
	}
	rows, err := s.q.ListLevelsByDepartment(ctx, departmentID)
	if err != nil {
		return nil, err
	}
	out := make([]Level, 0, len(rows))
	for _, r := range rows {
		out = append(out, levelFromRow(r))
	}
	return out, nil
}

func (s *Service) UpdateLevel(ctx context.Context, tenantID, departmentID, levelID uuid.UUID, in UpdateLevelInput) (Level, error) {
	if in.Name != nil && (*in.Name == "" || len(*in.Name) > 64) {
		return Level{}, ErrInvalidLevelName
	}
	if in.Rank != nil && *in.Rank <= 0 {
		return Level{}, ErrInvalidRank
	}
	if _, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: departmentID, TenantID: tenantID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Level{}, ErrDepartmentNotFound
		}
		return Level{}, err
	}
	row, err := s.q.UpdateDepartmentLevel(ctx, sqlc.UpdateDepartmentLevelParams{
		ID:   levelID,
		Name: in.Name,
		Rank: in.Rank,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Level{}, ErrLevelNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Level{}, ErrLevelAlreadyExists
		}
		return Level{}, err
	}
	return levelFromRow(row), nil
}

func (s *Service) DeleteLevel(ctx context.Context, tenantID, departmentID, levelID uuid.UUID) error {
	if _, err := s.q.GetDepartmentByID(ctx, sqlc.GetDepartmentByIDParams{ID: departmentID, TenantID: tenantID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDepartmentNotFound
		}
		return err
	}
	counts, err := s.q.CountDepartmentLevelReferences(ctx, levelID)
	if err != nil {
		return err
	}
	if counts > 0 {
		return &LevelInUseError{MembershipCount: counts}
	}
	n, err := s.q.DeleteDepartmentLevel(ctx, levelID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLevelNotFound
	}
	return nil
}

// Helpers ---------------------------------------------------------------

func departmentFromRow(r sqlc.Department) Department {
	out := Department{
		ID:          r.ID,
		TenantID:    r.TenantID,
		Name:        r.Name,
		Description: r.Description,
		CreatedAt:   r.CreatedAt.Time,
		UpdatedAt:   r.UpdatedAt.Time,
	}
	if r.DeprecatedAt.Valid {
		t := r.DeprecatedAt.Time
		out.DeprecatedAt = &t
	}
	return out
}

func levelFromRow(r sqlc.DepartmentLevel) Level {
	return Level{
		ID:           r.ID,
		DepartmentID: r.DepartmentID,
		Name:         r.Name,
		Rank:         r.Rank,
		CreatedAt:    r.CreatedAt.Time,
		UpdatedAt:    r.UpdatedAt.Time,
	}
}

// departmentFromFields is retained for callers that need to convert
// individual pgtype fields directly without a full sqlc.Department value.
func departmentFromFields(id, tenantID uuid.UUID, name, description string,
	deprecatedAt, createdAt, updatedAt pgtype.Timestamptz,
) Department {
	return departmentFromRow(sqlc.Department{
		ID:           id,
		TenantID:     tenantID,
		Name:         name,
		Description:  description,
		DeprecatedAt: deprecatedAt,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	})
}
