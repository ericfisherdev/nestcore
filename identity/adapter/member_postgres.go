package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// MemberRepository is the pgx-backed implementation of
// domain.MemberRepository.
type MemberRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.MemberRepository = (*MemberRepository)(nil)

// NewMemberRepository constructs the repository with an injected query
// executor. See HouseholdRepository's constructor doc for why the
// executor is a db.TX rather than a concrete pool type.
func NewMemberRepository(dbtx db.TX) *MemberRepository {
	if dbtx == nil {
		panic("adapter: NewMemberRepository requires a non-nil db.TX")
	}
	return &MemberRepository{dbtx: dbtx}
}

// CreateMember inserts a member, returning domain.ErrDuplicateMember when
// the display name collides (case-insensitively) within the household, and
// domain.ErrHouseholdNotFound when m.HouseholdID does not exist.
func (r *MemberRepository) CreateMember(ctx context.Context, m *domain.Member) error {
	if m == nil {
		return errors.New("adapter: create member: nil member")
	}
	const q = `
		INSERT INTO identity.member (id, household_id, display_name, role)
		VALUES ($1, $2, $3, $4)
		RETURNING active, created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q, m.ID.String(), m.HouseholdID.String(), m.DisplayName, m.Role.String()).
		Scan(&m.Active, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		switch {
		case isConstraintViolation(err, uniqueViolation, memberHouseholdNameUniq):
			return domain.ErrDuplicateMember
		case isConstraintViolation(err, foreignKeyViolation, memberHouseholdFK):
			return domain.ErrHouseholdNotFound
		}
		return fmt.Errorf("create member: %w", err)
	}
	return nil
}

// GetMember returns the member, or domain.ErrMemberNotFound.
func (r *MemberRepository) GetMember(ctx context.Context, id domain.MemberID) (*domain.Member, error) {
	const q = `
		SELECT id, household_id, display_name, role, active, created_at, updated_at
		FROM identity.member WHERE id = $1`
	m, err := scanMember(r.dbtx.QueryRow(ctx, q, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMemberNotFound
		}
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}

// ListMembers returns the household's members ordered by creation. An
// unknown household yields an empty slice, not an error.
func (r *MemberRepository) ListMembers(ctx context.Context, householdID domain.HouseholdID) ([]*domain.Member, error) {
	const q = `
		SELECT id, household_id, display_name, role, active, created_at, updated_at
		FROM identity.member WHERE household_id = $1 ORDER BY created_at, id`
	rows, err := r.dbtx.Query(ctx, q, householdID.String())
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	members := make([]*domain.Member, 0)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("list members: scan: %w", err)
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	return members, nil
}

// row abstracts pgx.Row and pgx.Rows for scanMember.
type row interface {
	Scan(dest ...any) error
}

func scanMember(r row) (*domain.Member, error) {
	var (
		m             domain.Member
		idStr, hidStr string
		roleStr       string
	)
	if err := r.Scan(&idStr, &hidStr, &m.DisplayName, &roleStr, &m.Active, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseMemberID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan member: %w", err)
	}
	hid, err := domain.ParseHouseholdID(hidStr)
	if err != nil {
		return nil, fmt.Errorf("scan member: %w", err)
	}
	role, err := domain.ParseRole(roleStr)
	if err != nil {
		return nil, fmt.Errorf("scan member: %w", err)
	}
	m.ID, m.HouseholdID, m.Role = id, hid, role
	return &m, nil
}
