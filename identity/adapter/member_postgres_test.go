package adapter_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

func newMemberTestRepos(t *testing.T) (*adapter.HouseholdRepository, *adapter.MemberRepository) {
	t.Helper()
	pool := newTestPool(t)
	return adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool)
}

func TestCreateListAndGetMembers(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Member Test Household")

	names := []string{"Maya", "Daniel", "Ivy"}
	var ids []domain.MemberID
	for _, name := range names {
		m := &domain.Member{ID: domain.NewMemberID(), HouseholdID: h.ID, DisplayName: name, Role: domain.RoleAdult}
		if err := members.CreateMember(testCtx(t), m); err != nil {
			t.Fatalf("CreateMember(%s): %v", name, err)
		}
		if !m.Active {
			t.Errorf("CreateMember(%s) Active = false, want true (the database default)", name)
		}
		ids = append(ids, m.ID)
	}

	got, err := members.ListMembers(testCtx(t), h.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(got) != len(names) {
		t.Fatalf("ListMembers returned %d, want %d", len(got), len(names))
	}
	// Insertion order is preserved.
	if got[0].DisplayName != "Maya" || got[1].DisplayName != "Daniel" {
		t.Errorf("ListMembers order = [%s, %s], want [Maya, Daniel]", got[0].DisplayName, got[1].DisplayName)
	}

	one, err := members.GetMember(testCtx(t), ids[0])
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if one.DisplayName != "Maya" || one.Role != domain.RoleAdult {
		t.Errorf("GetMember = %+v, want DisplayName=Maya Role=adult", one)
	}
}

func TestCreateMemberDuplicateName(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Duplicate Name Household")

	first := &domain.Member{ID: domain.NewMemberID(), HouseholdID: h.ID, DisplayName: "Maya", Role: domain.RoleAdult}
	if err := members.CreateMember(testCtx(t), first); err != nil {
		t.Fatalf("CreateMember(first): %v", err)
	}
	// Case-insensitive duplicate must be rejected.
	dup := &domain.Member{ID: domain.NewMemberID(), HouseholdID: h.ID, DisplayName: "MAYA", Role: domain.RoleChild}
	if err := members.CreateMember(testCtx(t), dup); !errors.Is(err, domain.ErrDuplicateMember) {
		t.Errorf("CreateMember(duplicate) error = %v, want ErrDuplicateMember", err)
	}
}

// TestCreateMemberInvalidRole proves CreateMember rejects an invalid or
// zero-value Role before it ever reaches the database, rather than
// surfacing an opaque SQLSTATE 23514 from the baseline migration's
// unnamed role CHECK constraint.
func TestCreateMemberInvalidRole(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Invalid Role Household")

	for name, role := range map[string]domain.Role{
		"unknown value": domain.Role("admin"),
		"zero value":    domain.Role(""),
	} {
		t.Run(name, func(t *testing.T) {
			m := &domain.Member{ID: domain.NewMemberID(), HouseholdID: h.ID, DisplayName: name, Role: role}
			if err := members.CreateMember(testCtx(t), m); err == nil {
				t.Fatalf("CreateMember(role=%q) error = nil, want non-nil", role)
			}
		})
	}
}

func TestCreateMemberUnknownHousehold(t *testing.T) {
	_, members := newMemberTestRepos(t)
	m := &domain.Member{
		ID:          domain.NewMemberID(),
		HouseholdID: domain.NewHouseholdID(), // not persisted
		DisplayName: "Orphan",
		Role:        domain.RoleAdult,
	}
	if err := members.CreateMember(testCtx(t), m); !errors.Is(err, domain.ErrHouseholdNotFound) {
		t.Errorf("CreateMember(unknown household) error = %v, want ErrHouseholdNotFound", err)
	}
}

func TestListMembersUnknownHousehold(t *testing.T) {
	_, members := newMemberTestRepos(t)
	// ListMembers fails open: an unknown household yields an empty slice,
	// not an error (documented contract).
	got, err := members.ListMembers(testCtx(t), domain.NewHouseholdID())
	if err != nil {
		t.Fatalf("ListMembers(unknown) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMembers(unknown) returned %d members, want 0", len(got))
	}
}

func TestGetMemberNotFound(t *testing.T) {
	_, members := newMemberTestRepos(t)
	if _, err := members.GetMember(testCtx(t), domain.NewMemberID()); !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("GetMember(unknown) error = %v, want ErrMemberNotFound", err)
	}
}
