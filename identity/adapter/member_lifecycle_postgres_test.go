package adapter_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// seedMember creates a member with the given role in h, failing the test
// on error.
func seedMemberWithRole(t *testing.T, members *adapter.MemberRepository, h *domain.Household, name string, role domain.Role) *domain.Member {
	t.Helper()
	m := &domain.Member{ID: domain.NewMemberID(), HouseholdID: h.ID, DisplayName: name, Role: role}
	if err := members.CreateMember(testCtx(t), m); err != nil {
		t.Fatalf("CreateMember(%s): %v", name, err)
	}
	return m
}

func TestUpdateMemberProfileRename(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Rename Household")
	owner := seedMemberWithRole(t, members, h, "Original Name", domain.RoleOwner)

	if err := members.UpdateMemberProfile(testCtx(t), h.ID, owner.ID, "New Name", domain.RoleOwner); err != nil {
		t.Fatalf("UpdateMemberProfile: %v", err)
	}

	got, err := members.GetMember(testCtx(t), owner.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.DisplayName != "New Name" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "New Name")
	}
}

func TestUpdateMemberProfileRoleChangeVisible(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Role Change Household")
	seedMemberWithRole(t, members, h, "Owner", domain.RoleOwner)
	child := seedMemberWithRole(t, members, h, "Kid", domain.RoleChild)

	if err := members.UpdateMemberProfile(testCtx(t), h.ID, child.ID, "Kid", domain.RoleAdult); err != nil {
		t.Fatalf("UpdateMemberProfile (promote): %v", err)
	}

	got, err := members.GetMember(testCtx(t), child.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.Role != domain.RoleAdult {
		t.Errorf("Role = %q, want %q", got.Role, domain.RoleAdult)
	}
}

func TestUpdateMemberProfileDuplicateName(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Duplicate Rename Household")
	seedMemberWithRole(t, members, h, "Maya", domain.RoleOwner)
	other := seedMemberWithRole(t, members, h, "Daniel", domain.RoleAdult)

	// Case-insensitive collision must be rejected, mirroring CreateMember.
	err := members.UpdateMemberProfile(testCtx(t), h.ID, other.ID, "MAYA", domain.RoleAdult)
	if !errors.Is(err, domain.ErrDuplicateMember) {
		t.Errorf("UpdateMemberProfile(duplicate name) error = %v, want ErrDuplicateMember", err)
	}
}

func TestUpdateMemberProfileNotFound(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Not Found Household")

	err := members.UpdateMemberProfile(testCtx(t), h.ID, domain.NewMemberID(), "Nobody", domain.RoleAdult)
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("UpdateMemberProfile(unknown id) error = %v, want ErrMemberNotFound", err)
	}
}

func TestUpdateMemberProfileWrongHouseholdNotFound(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h1 := seedHousehold(t, households, "Household One")
	h2 := seedHousehold(t, households, "Household Two")
	member := seedMemberWithRole(t, members, h1, "Maya", domain.RoleOwner)

	// member belongs to h1, not h2: scoping by (householdID, id) together
	// must treat this exactly like an unknown id.
	err := members.UpdateMemberProfile(testCtx(t), h2.ID, member.ID, "Maya", domain.RoleAdult)
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("UpdateMemberProfile(wrong household) error = %v, want ErrMemberNotFound", err)
	}
}

func TestUpdateMemberProfileDemoteLastActiveOwnerRefused(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Sole Owner Household")
	owner := seedMemberWithRole(t, members, h, "Sole Owner", domain.RoleOwner)

	// The display name is deliberately different from the seeded one: if
	// the refusal only rejected the role change but silently applied the
	// rename half of the statement, re-asserting the SAME name back would
	// not catch it. The whole row update is one atomic statement.
	err := members.UpdateMemberProfile(testCtx(t), h.ID, owner.ID, "Renamed But Refused", domain.RoleAdult)
	if !errors.Is(err, domain.ErrLastActiveOwner) {
		t.Errorf("UpdateMemberProfile(demote sole owner) error = %v, want ErrLastActiveOwner", err)
	}

	got, err := members.GetMember(testCtx(t), owner.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.Role != domain.RoleOwner {
		t.Errorf("Role after refused demotion = %q, want unchanged %q", got.Role, domain.RoleOwner)
	}
	if got.DisplayName != "Sole Owner" {
		t.Errorf("DisplayName after refused demotion = %q, want unchanged %q", got.DisplayName, "Sole Owner")
	}
}

func TestUpdateMemberProfileDemoteAllowedWithAnotherActiveOwner(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Two Owner Household")
	seedMemberWithRole(t, members, h, "Owner A", domain.RoleOwner)
	ownerB := seedMemberWithRole(t, members, h, "Owner B", domain.RoleOwner)

	if err := members.UpdateMemberProfile(testCtx(t), h.ID, ownerB.ID, "Owner B", domain.RoleAdult); err != nil {
		t.Fatalf("UpdateMemberProfile(demote with another active owner): %v", err)
	}

	got, err := members.GetMember(testCtx(t), ownerB.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.Role != domain.RoleAdult {
		t.Errorf("Role = %q, want %q", got.Role, domain.RoleAdult)
	}
}

func TestUpdateMemberProfileDemoteAllowedWhenOtherOwnerIsInactive(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Inactive Owner Household")
	other := seedMemberWithRole(t, members, h, "Deactivated Owner", domain.RoleOwner)
	sole := seedMemberWithRole(t, members, h, "Active Owner", domain.RoleOwner)

	if err := members.SetMemberActive(testCtx(t), h.ID, other.ID, false); err != nil {
		t.Fatalf("SetMemberActive(deactivate other): %v", err)
	}

	// An inactive owner does not count toward the guard, so demoting the
	// only remaining ACTIVE owner must still be refused.
	err := members.UpdateMemberProfile(testCtx(t), h.ID, sole.ID, "Active Owner", domain.RoleAdult)
	if !errors.Is(err, domain.ErrLastActiveOwner) {
		t.Errorf("UpdateMemberProfile(demote last active owner, other inactive) error = %v, want ErrLastActiveOwner", err)
	}
}

func TestSetMemberActiveDeactivateLastActiveOwnerRefused(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Deactivate Sole Owner Household")
	owner := seedMemberWithRole(t, members, h, "Sole Owner", domain.RoleOwner)

	err := members.SetMemberActive(testCtx(t), h.ID, owner.ID, false)
	if !errors.Is(err, domain.ErrLastActiveOwner) {
		t.Errorf("SetMemberActive(deactivate sole owner) error = %v, want ErrLastActiveOwner", err)
	}

	got, err := members.GetMember(testCtx(t), owner.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if !got.Active {
		t.Error("Active after refused deactivation = false, want unchanged true")
	}
}

func TestSetMemberActiveDeactivateAllowedWithAnotherActiveOwner(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Deactivate With Another Owner Household")
	seedMemberWithRole(t, members, h, "Owner A", domain.RoleOwner)
	ownerB := seedMemberWithRole(t, members, h, "Owner B", domain.RoleOwner)

	if err := members.SetMemberActive(testCtx(t), h.ID, ownerB.ID, false); err != nil {
		t.Fatalf("SetMemberActive(deactivate with another active owner): %v", err)
	}

	got, err := members.GetMember(testCtx(t), ownerB.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.Active {
		t.Error("Active after deactivation = true, want false")
	}
}

func TestSetMemberActiveDeactivateNonOwnerAlwaysAllowed(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Deactivate Non Owner Household")
	seedMemberWithRole(t, members, h, "Owner", domain.RoleOwner)
	adult := seedMemberWithRole(t, members, h, "Adult", domain.RoleAdult)

	if err := members.SetMemberActive(testCtx(t), h.ID, adult.ID, false); err != nil {
		t.Fatalf("SetMemberActive(deactivate adult): %v", err)
	}

	got, err := members.GetMember(testCtx(t), adult.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if got.Active {
		t.Error("Active after deactivation = true, want false")
	}
}

func TestSetMemberActiveReactivateRoundTrip(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Reactivate Household")
	seedMemberWithRole(t, members, h, "Owner", domain.RoleOwner)
	member := seedMemberWithRole(t, members, h, "Member", domain.RoleAdult)

	if err := members.SetMemberActive(testCtx(t), h.ID, member.ID, false); err != nil {
		t.Fatalf("SetMemberActive(deactivate): %v", err)
	}
	if err := members.SetMemberActive(testCtx(t), h.ID, member.ID, true); err != nil {
		t.Fatalf("SetMemberActive(reactivate): %v", err)
	}

	got, err := members.GetMember(testCtx(t), member.ID)
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if !got.Active {
		t.Error("Active after reactivation = false, want true")
	}
}

func TestSetMemberActiveReactivateSoleOwnerNeverRefused(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Reactivate Sole Owner Household")
	seedMemberWithRole(t, members, h, "Owner A", domain.RoleOwner)
	ownerB := seedMemberWithRole(t, members, h, "Owner B", domain.RoleOwner)

	if err := members.SetMemberActive(testCtx(t), h.ID, ownerB.ID, false); err != nil {
		t.Fatalf("SetMemberActive(deactivate): %v", err)
	}
	// Reactivating an owner is never refused by the guard, even though
	// reactivating necessarily changes the count of active owners.
	if err := members.SetMemberActive(testCtx(t), h.ID, ownerB.ID, true); err != nil {
		t.Fatalf("SetMemberActive(reactivate owner) error = %v, want nil", err)
	}
}

func TestSetMemberActiveNotFound(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Set Active Not Found Household")

	err := members.SetMemberActive(testCtx(t), h.ID, domain.NewMemberID(), false)
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("SetMemberActive(unknown id) error = %v, want ErrMemberNotFound", err)
	}
}

func TestSetMemberActiveWrongHouseholdNotFound(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h1 := seedHousehold(t, households, "Household Alpha")
	h2 := seedHousehold(t, households, "Household Beta")
	member := seedMemberWithRole(t, members, h1, "Maya", domain.RoleAdult)

	err := members.SetMemberActive(testCtx(t), h2.ID, member.ID, false)
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("SetMemberActive(wrong household) error = %v, want ErrMemberNotFound", err)
	}
}

// TestGuardIsPerHousehold proves the last-active-owner guard never counts
// an owner in a DIFFERENT household: a household with exactly one active
// owner must still refuse to demote/deactivate that owner even while
// another household (with its own sole active owner) exists alongside it.
func TestGuardIsPerHousehold(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h1 := seedHousehold(t, households, "Isolated Household One")
	h2 := seedHousehold(t, households, "Isolated Household Two")
	owner1 := seedMemberWithRole(t, members, h1, "Owner One", domain.RoleOwner)
	seedMemberWithRole(t, members, h2, "Owner Two", domain.RoleOwner)

	err := members.SetMemberActive(testCtx(t), h1.ID, owner1.ID, false)
	if !errors.Is(err, domain.ErrLastActiveOwner) {
		t.Errorf("SetMemberActive(sole owner, other household has its own owner) error = %v, want ErrLastActiveOwner", err)
	}
}

func TestUpdateMemberProfileInvalidRole(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Invalid Role Update Household")
	member := seedMemberWithRole(t, members, h, "Maya", domain.RoleAdult)

	if err := members.UpdateMemberProfile(testCtx(t), h.ID, member.ID, "Maya", domain.Role("admin")); err == nil {
		t.Fatal("UpdateMemberProfile(invalid role) error = nil, want non-nil")
	}
}

// TestSetMemberActiveConcurrentDeactivationsSerializeWithoutDeadlock
// deactivates the household's two owners at the same time, from two
// goroutines racing against the same pool, released together through a
// barrier so their two guarded UPDATEs are actually in flight
// simultaneously rather than ordered by goroutine-startup jitter (without
// it, one call typically completes and commits well before the other
// even begins, and the assertions below would hold for reasons unrelated
// to the guard's own serialization).
//
// The household-scoped advisory lock each guarded UPDATE takes (see
// memberTxBeginner's doc) must serialize the two rather than let them
// cross-lock each other's target row, AND must give the second one a
// fresh-enough snapshot to see the first's commit: exactly one succeeds
// and the other is refused with ErrLastActiveOwner, and — checked
// directly against the stored rows, not just inferred from the returned
// errors — the household has exactly one active owner left afterward. A
// real Postgres deadlock (SQLSTATE 40P01) would instead surface as a
// third, distinct wrapped error on whichever side lost Postgres's
// deadlock detector's coin flip; a stale-snapshot visibility gap would
// instead let both succeed, leaving zero active owners.
func TestSetMemberActiveConcurrentDeactivationsSerializeWithoutDeadlock(t *testing.T) {
	households, members := newMemberTestRepos(t)
	h := seedHousehold(t, households, "Concurrent Deactivation Household")
	ownerA := seedMemberWithRole(t, members, h, "Owner A", domain.RoleOwner)
	ownerB := seedMemberWithRole(t, members, h, "Owner B", domain.RoleOwner)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs[0] = members.SetMemberActive(testCtx(t), h.ID, ownerA.ID, false)
	}()
	go func() {
		defer wg.Done()
		<-start
		errs[1] = members.SetMemberActive(testCtx(t), h.ID, ownerB.ID, false)
	}()
	close(start)
	wg.Wait()

	var succeeded, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrLastActiveOwner):
			refused++
		default:
			t.Errorf("concurrent SetMemberActive error = %v, want nil or ErrLastActiveOwner", err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Errorf("concurrent SetMemberActive: succeeded=%d refused=%d, want exactly 1 of each", succeeded, refused)
	}

	all, err := members.ListMembers(testCtx(t), h.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	activeOwners := 0
	for _, m := range all {
		if m.Role == domain.RoleOwner && m.Active {
			activeOwners++
		}
	}
	if activeOwners != 1 {
		t.Errorf("active owners after concurrent deactivation = %d, want exactly 1", activeOwners)
	}
}
