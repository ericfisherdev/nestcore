package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestcore/identity/domain"
)

func TestRoleParse(t *testing.T) {
	for _, s := range []string{"owner", "adult", "child"} {
		if r, err := domain.ParseRole(s); err != nil || r.String() != s {
			t.Errorf("ParseRole(%q) = (%q, %v), want valid", s, r, err)
		}
	}
	if _, err := domain.ParseRole("admin"); err == nil {
		t.Error("ParseRole(admin) = nil error, want error")
	}
}

// TestRoleCanAdminister proves the derivation helper across all three
// values: owner and adult administer, child does not.
func TestRoleCanAdminister(t *testing.T) {
	tests := []struct {
		role domain.Role
		want bool
	}{
		{domain.RoleOwner, true},
		{domain.RoleAdult, true},
		{domain.RoleChild, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			if got := tt.role.CanAdminister(); got != tt.want {
				t.Errorf("%s.CanAdminister() = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestIDRoundTrip(t *testing.T) {
	hid := domain.NewHouseholdID()
	got, err := domain.ParseHouseholdID(hid.String())
	if err != nil || got != hid {
		t.Errorf("household id round-trip = (%v, %v), want %v", got, err, hid)
	}
	mid := domain.NewMemberID()
	gotMID, err := domain.ParseMemberID(mid.String())
	if err != nil || gotMID != mid {
		t.Errorf("member id round-trip = (%v, %v), want %v", gotMID, err, mid)
	}
	if _, err := domain.ParseMemberID("not-a-uuid"); err == nil {
		t.Error("ParseMemberID(not-a-uuid) = nil error, want error")
	}
	if _, err := domain.ParseHouseholdID("not-a-uuid"); err == nil {
		t.Error("ParseHouseholdID(not-a-uuid) = nil error, want error")
	}
}
