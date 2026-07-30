package adapter_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

func seedHousehold(t *testing.T, repo *adapter.HouseholdRepository, name string) *domain.Household {
	t.Helper()
	h := &domain.Household{ID: domain.NewHouseholdID(), Name: name}
	if err := repo.CreateHousehold(testCtx(t), h); err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}
	return h
}

func TestCreateAndGetHousehold(t *testing.T) {
	repo := adapter.NewHouseholdRepository(newTestPool(t))
	h := seedHousehold(t, repo, "The Fishers")

	got, err := repo.GetHousehold(testCtx(t), h.ID)
	if err != nil {
		t.Fatalf("GetHousehold: %v", err)
	}
	if got.ID != h.ID || got.Name != "The Fishers" {
		t.Errorf("GetHousehold = %+v, want id %v name %q", got, h.ID, "The Fishers")
	}
	if got.CreatedAt.IsZero() {
		t.Error("GetHousehold returned zero CreatedAt")
	}
}

func TestGetHouseholdNotFound(t *testing.T) {
	repo := adapter.NewHouseholdRepository(newTestPool(t))

	if _, err := repo.GetHousehold(testCtx(t), domain.NewHouseholdID()); !errors.Is(err, domain.ErrHouseholdNotFound) {
		t.Errorf("GetHousehold(unknown) error = %v, want ErrHouseholdNotFound", err)
	}
}
