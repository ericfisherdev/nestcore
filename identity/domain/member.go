package domain

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Role is a member's role within a household, unified across Nestova and
// Nestorage (epic NSTR-112): apps derive their own admin-vs-member
// behavior from these three values and never store a role vocabulary of
// their own — an admin/member vocabulary would flatten child into adult
// and lose that distinction permanently. Stored as text, validated here.
type Role string

// Member roles.
const (
	RoleOwner Role = "owner"
	RoleAdult Role = "adult"
	RoleChild Role = "child"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdult, RoleChild:
		return true
	default:
		return false
	}
}

// CanAdminister reports whether r carries household-admin privileges
// (owner or adult) — the derivation Nestorage (and any future consumer)
// uses instead of storing its own admin/member flag.
func (r Role) CanAdminister() bool {
	return r == RoleOwner || r == RoleAdult
}

// String returns the role's stored value.
func (r Role) String() string { return string(r) }

// ParseRole validates and returns a Role, or an error for an unknown value.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("identity: invalid role %q", s)
	}
	return r, nil
}

// Member is a person in a household. It is a child entity of the
// Household aggregate root. Email and password-hash credential state are
// deliberately not part of this type — they are Credential's concern,
// looked up and written through CredentialRepository, mirroring the
// household/auth split this schema's design is ported from.
type Member struct {
	ID          MemberID
	HouseholdID HouseholdID
	DisplayName string
	Role        Role
	// Active is the IDENTITY-level deactivation flag: false cuts a
	// member's access to every app sharing this schema. This package only
	// reads and writes it as a plain field; the deactivation guards that
	// consume it belong to NSTR-111.
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Domain errors returned by MemberRepository implementations.
var (
	// ErrMemberNotFound is returned when a member does not exist.
	ErrMemberNotFound = errors.New("identity: member not found")
	// ErrDuplicateMember is returned when adding a member whose display
	// name already exists (case-insensitively) within the household.
	ErrDuplicateMember = errors.New("identity: duplicate member display name in household")
	// ErrLastActiveOwner is returned by MemberWriter when a role change or
	// deactivation would leave a household with no active owner. It is
	// never returned for reactivation — reactivating only ever adds an
	// active owner back, so it cannot violate this invariant.
	ErrLastActiveOwner = errors.New("identity: household must keep at least one active owner")
)

// MemberRepository persists members and looks them up. It depends only on
// HouseholdID/MemberID from this same package (ISP): callers needing
// credential state depend on CredentialRepository instead, not on this
// port.
//
// Persistence contracts:
//   - CreateMember expects m.ID, m.HouseholdID, m.DisplayName, and a valid
//     m.Role set; it populates CreatedAt/UpdatedAt on m. Active defaults to
//     true at the database, so a freshly created member reads back Active
//     true regardless of the zero value passed in.
//
// Error contracts:
//   - CreateMember returns ErrDuplicateMember when the display name
//     collides (case-insensitively) within the household, and
//     ErrHouseholdNotFound when m.HouseholdID does not exist.
//   - GetMember returns ErrMemberNotFound when id is unknown.
//   - ListMembers returns an empty slice (not an error) for an unknown
//     household.
type MemberRepository interface {
	CreateMember(ctx context.Context, m *Member) error
	GetMember(ctx context.Context, id MemberID) (*Member, error)
	ListMembers(ctx context.Context, householdID HouseholdID) ([]*Member, error)
}

// MemberWriter is the outbound port for the member lifecycle mutations
// (NSTR-111): rename, role change, deactivate, and reactivate. It is kept
// separate from MemberRepository (ISP) so the existing read-side fakes
// this package's consumers already have keep compiling unchanged — they
// were never asked to implement writes.
//
// Both methods scope by (householdID, id) together, not id alone: a
// mismatched pair (an id that exists but belongs to a different household)
// is indistinguishable from an unknown id, exactly like MemberRepository's
// own GetMember would behave if it were household-scoped.
//
// Error contracts:
//   - UpdateMemberProfile returns ErrMemberNotFound when id does not exist
//     in householdID, ErrDuplicateMember when displayName collides
//     case-insensitively with another member in the household, and
//     ErrLastActiveOwner when the role change would leave the household
//     with no active owner (i.e. id is currently the household's sole
//     active owner and role is not RoleOwner).
//   - SetMemberActive returns ErrMemberNotFound when id does not exist in
//     householdID, and ErrLastActiveOwner when active is false and id is
//     the household's sole active owner. Reactivation (active true) is
//     never refused by the guard.
type MemberWriter interface {
	UpdateMemberProfile(ctx context.Context, householdID HouseholdID, id MemberID, displayName string, role Role) error
	SetMemberActive(ctx context.Context, householdID HouseholdID, id MemberID, active bool) error
}
