package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// CredentialRepository is the pgx-backed implementation of
// domain.CredentialRepository, backed by identity.member's own email and
// password_hash columns (see identity/migrate's baseline migration, not a
// separate credential table).
type CredentialRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.CredentialRepository = (*CredentialRepository)(nil)

// NewCredentialRepository constructs the repository with an injected
// query executor. See HouseholdRepository's constructor doc for why the
// executor is a db.TX rather than a concrete pool type.
func NewCredentialRepository(dbtx db.TX) *CredentialRepository {
	if dbtx == nil {
		panic("adapter: NewCredentialRepository requires a non-nil db.TX")
	}
	return &CredentialRepository{dbtx: dbtx}
}

// FindByEmail returns the Credential for the given email address, or
// domain.ErrInvalidCredentials when no member with that email and a
// non-null password_hash exists (preventing user enumeration).
func (r *CredentialRepository) FindByEmail(ctx context.Context, email string) (*domain.Credential, error) {
	const q = `
		SELECT id, password_hash
		  FROM identity.member
		 WHERE email = $1
		   AND password_hash IS NOT NULL`

	var (
		idStr        string
		passwordHash string
	)
	err := r.dbtx.QueryRow(ctx, q, email).Scan(&idStr, &passwordHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("find by email: %w", err)
	}

	memberID, err := domain.ParseMemberID(idStr)
	if err != nil {
		return nil, fmt.Errorf("find by email: parse member id: %w", err)
	}

	return &domain.Credential{MemberID: memberID, PasswordHash: passwordHash}, nil
}

// SetCredential stores (or replaces) the email and password hash on the
// member row identified by memberID. Returns domain.ErrMemberNotFound
// when the member does not exist, and domain.ErrEmailAlreadyInUse when
// the email is already assigned to another member.
func (r *CredentialRepository) SetCredential(ctx context.Context, memberID domain.MemberID, email, passwordHash string) error {
	const q = `
		UPDATE identity.member
		   SET email         = $2,
		       password_hash = $3,
		       updated_at    = now()
		 WHERE id = $1`

	tag, err := r.dbtx.Exec(ctx, q, memberID.String(), email, passwordHash)
	if err != nil {
		if isConstraintViolation(err, uniqueViolation, memberEmailUnique) {
			return domain.ErrEmailAlreadyInUse
		}
		return fmt.Errorf("set credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrMemberNotFound
	}
	return nil
}
