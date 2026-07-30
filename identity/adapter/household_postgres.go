package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// HouseholdRepository is the pgx-backed implementation of
// domain.HouseholdRepository. UUIDs are passed and scanned as text,
// matching the identity schema's existing adapter convention (no pgx
// UUID codec registration).
type HouseholdRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.HouseholdRepository = (*HouseholdRepository)(nil)

// NewHouseholdRepository constructs the repository with an injected query
// executor. The executor is a db.TX, satisfied by both *pgxpool.Pool (the
// default composition) and pgx.Tx (so a caller can run this repository
// inside its own transaction); the same methods work against either.
func NewHouseholdRepository(dbtx db.TX) *HouseholdRepository {
	if dbtx == nil {
		panic("adapter: NewHouseholdRepository requires a non-nil db.TX")
	}
	return &HouseholdRepository{dbtx: dbtx}
}

// CreateHousehold inserts a household and populates its timestamps.
func (r *HouseholdRepository) CreateHousehold(ctx context.Context, h *domain.Household) error {
	if h == nil {
		return errors.New("adapter: create household: nil household")
	}
	const q = `INSERT INTO identity.household (id, name) VALUES ($1, $2) RETURNING created_at, updated_at`
	if err := r.dbtx.QueryRow(ctx, q, h.ID.String(), h.Name).Scan(&h.CreatedAt, &h.UpdatedAt); err != nil {
		return fmt.Errorf("create household: %w", err)
	}
	return nil
}

// GetHousehold returns the household, or domain.ErrHouseholdNotFound.
func (r *HouseholdRepository) GetHousehold(ctx context.Context, id domain.HouseholdID) (*domain.Household, error) {
	const q = `SELECT id, name, created_at, updated_at FROM identity.household WHERE id = $1`

	var (
		h     domain.Household
		idStr string
	)
	err := r.dbtx.QueryRow(ctx, q, id.String()).Scan(&idStr, &h.Name, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrHouseholdNotFound
		}
		return nil, fmt.Errorf("get household: %w", err)
	}
	parsedID, err := domain.ParseHouseholdID(idStr)
	if err != nil {
		return nil, fmt.Errorf("get household: parse household id: %w", err)
	}
	h.ID = parsedID
	return &h, nil
}
