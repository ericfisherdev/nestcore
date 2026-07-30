package domain

import (
	"context"
	"errors"
	"time"
)

// Household is the aggregate root for the identity bounded context.
// Presentation fields (e.g. Nestova's quiet hours, Nestorage's own
// per-app settings) stay out of this type — see identity/migrate's
// package doc for the app-side presentation boundary this schema
// enforces.
type Household struct {
	ID        HouseholdID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrHouseholdNotFound is returned when a household does not exist.
var ErrHouseholdNotFound = errors.New("identity: household not found")

// HouseholdRepository is the outbound port for persisting and retrieving
// households. Implementations live in identity/adapter.
//
// Error contracts:
//   - CreateHousehold expects h.ID and h.Name set; it populates
//     CreatedAt/UpdatedAt on h and surfaces any other failure (e.g. an id
//     collision) as a wrapped error, not a sentinel.
//   - GetHousehold returns ErrHouseholdNotFound when id is unknown.
type HouseholdRepository interface {
	CreateHousehold(ctx context.Context, h *Household) error
	GetHousehold(ctx context.Context, id HouseholdID) (*Household, error)
}
