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

// Compile-time assurance the adapter satisfies both ports.
var (
	_ domain.MemberRepository = (*MemberRepository)(nil)
	_ domain.MemberWriter     = (*MemberRepository)(nil)
)

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
	if !m.Role.Valid() {
		return fmt.Errorf("adapter: create member: invalid role %q", m.Role)
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

// householdWriteLockSQL is a CTE, prefixed onto UpdateMemberProfile's and
// SetMemberActive's guarded UPDATE, that takes a household-scoped
// transaction-duration advisory lock before the UPDATE touches any row.
// The enclosing statement's "FROM household_lock" (with no join condition,
// against this single-row CTE) is what forces Postgres to actually
// evaluate it rather than treat it as dead code: an unreferenced CTE has
// no such guarantee, but joining a one-row relation into the UPDATE's own
// FROM leaves its row set unchanged while still requiring the CTE to run
// first.
//
// This lock is what makes the last-active-owner guard (below) race-safe
// rather than a check-then-write, AND why the guard's own EXISTS subquery
// needs no locking of its own: earlier revisions of this guard used a
// correlated "FOR UPDATE" on the candidate "other owner" rows instead, but
// that deadlocks under concurrency — demoting owner A takes A's row lock
// (as the UPDATE's own target) and then wants a FOR UPDATE lock on B (as
// a candidate "other" owner), while a concurrent demotion of B takes B's
// row lock and wants a lock on A: two transactions each holding what the
// other wants, in reverse order, is a textbook deadlock, and Postgres
// aborts one of them with a 40P01 error rather than serializing them.
// Serializing on ONE advisory lock per household up front instead means
// the second transaction blocks before taking any row lock at all, so the
// two can never hold conflicting locks in opposite orders.
//
// hashtext's 32-bit range makes a false collision between two different
// households' lock keys possible in principle; the only cost of one is a
// spurious moment of extra serialization between two unrelated
// households' guarded mutations, never an incorrect result.
const householdWriteLockSQL = `
	WITH household_lock AS (
		SELECT pg_advisory_xact_lock(hashtext($2)) AS locked
	)`

// lastActiveOwnerGuardSQL is the fragment shared by UpdateMemberProfile and
// SetMemberActive: it evaluates to true exactly when the pending change
// would demote or deactivate the household's sole active owner, in which
// case the enclosing UPDATE's WHERE clause must reject the row. Every
// query embedding it, together with householdWriteLockSQL, must bind $1
// to the member id and $2 to the household id.
const lastActiveOwnerGuardSQL = `
	NOT EXISTS (
		SELECT 1 FROM identity.member other
		 WHERE other.household_id = $2::uuid AND other.id <> $1
		   AND other.role = 'owner' AND other.active
	)`

// UpdateMemberProfile renames a member and/or changes their role, subject
// to the last-active-owner guard documented on domain.MemberWriter.
func (r *MemberRepository) UpdateMemberProfile(ctx context.Context, householdID domain.HouseholdID, id domain.MemberID, displayName string, role domain.Role) error {
	if !role.Valid() {
		return fmt.Errorf("adapter: update member profile: invalid role %q", role)
	}
	q := householdWriteLockSQL + `
		UPDATE identity.member AS m
		   SET display_name = $3, role = $4, updated_at = now()
		  FROM household_lock
		 WHERE m.id = $1 AND m.household_id = $2::uuid
		   AND NOT (
		         m.role = 'owner' AND m.active AND $4 <> 'owner' AND ` + lastActiveOwnerGuardSQL + `
		   )`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), householdID.String(), displayName, role.String())
	if err != nil {
		if isConstraintViolation(err, uniqueViolation, memberHouseholdNameUniq) {
			return domain.ErrDuplicateMember
		}
		return fmt.Errorf("update member profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return r.memberWriteFailureReason(ctx, householdID, id)
	}
	return nil
}

// SetMemberActive deactivates or reactivates a member, subject to the
// last-active-owner guard documented on domain.MemberWriter. Reactivation
// (active true) is never refused: the guard fragment's leading
// "$3 = false" clause is false in that case, so the surrounding NOT(...)
// is unconditionally true and the row updates.
func (r *MemberRepository) SetMemberActive(ctx context.Context, householdID domain.HouseholdID, id domain.MemberID, active bool) error {
	q := householdWriteLockSQL + `
		UPDATE identity.member AS m
		   SET active = $3, updated_at = now()
		  FROM household_lock
		 WHERE m.id = $1 AND m.household_id = $2::uuid
		   AND NOT (
		         $3 = false AND m.role = 'owner' AND m.active AND ` + lastActiveOwnerGuardSQL + `
		   )`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), householdID.String(), active)
	if err != nil {
		return fmt.Errorf("set member active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return r.memberWriteFailureReason(ctx, householdID, id)
	}
	return nil
}

// memberWriteFailureReason distinguishes, after an UpdateMemberProfile or
// SetMemberActive UPDATE matched zero rows, whether id simply does not
// exist in householdID (ErrMemberNotFound) or the last-active-owner guard
// rejected the change (ErrLastActiveOwner). Members are never deleted
// (identity/migrate's package doc: deactivation, not deletion), so this
// existence check cannot itself race the UPDATE it is explaining.
func (r *MemberRepository) memberWriteFailureReason(ctx context.Context, householdID domain.HouseholdID, id domain.MemberID) error {
	const q = `SELECT 1 FROM identity.member WHERE id = $1 AND household_id = $2`
	var exists int
	err := r.dbtx.QueryRow(ctx, q, id.String(), householdID.String()).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrMemberNotFound
		}
		return fmt.Errorf("determine member write failure reason: %w", err)
	}
	return domain.ErrLastActiveOwner
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
