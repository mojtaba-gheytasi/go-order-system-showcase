package application_test

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

func testCommand() application.ReserveStockCommand {
	return application.ReserveStockCommand{
		OrderID: testOrderID,
		Lines: []application.Line{
			{ProductSKU: "SKU-A", Quantity: 2},
		},
	}
}

func TestReserveStockClaimsWhenNothingIsHeld(t *testing.T) {
	store := &fakeStore{}

	require.NoError(t, useCase(store).Execute(context.Background(), testCommand()))
	assert.Equal(t, 1, store.claims)
}

func TestReserveStockAcceptsARepeatOfWhatIsAlreadyHeld(t *testing.T) {
	store := &fakeStore{held: []application.Line{{ProductSKU: "SKU-A", Quantity: 2}}}

	require.NoError(t, useCase(store).Execute(context.Background(), testCommand()))
	assert.Equal(t, 0, store.claims, "a repeat must not hold the stock again")
}

// A failed attempt holds nothing and records nothing, so retrying is a fresh
// attempt. If the warehouse was restocked in between, the same order id now
// succeeds — which is the point of not recording failures.
func TestReserveStockRetriesAFailedAttemptFromScratch(t *testing.T) {
	store := &fakeStore{claimErr: application.ErrInsufficientStock}

	err := useCase(store).Execute(context.Background(), testCommand())
	require.ErrorIs(t, err, application.ErrInsufficientStock)

	store.claimErr = nil

	require.NoError(t, useCase(store).Execute(context.Background(), testCommand()))
}

// Reporting success here would confirm stock for products nobody reserved.
func TestReserveStockRefusesTheSameOrderIDWithDifferentProducts(t *testing.T) {
	store := &fakeStore{held: []application.Line{{ProductSKU: "SKU-A", Quantity: 99}}}

	err := useCase(store).Execute(context.Background(), testCommand())

	require.ErrorIs(t, err, application.ErrIdempotencyConflict)
	assert.Equal(t, 0, store.claims)
}

// Two requests carrying the same order id race; the loser's claim is refused and
// it must answer from what the winner holds rather than failing.
func TestReserveStockRereadsAfterLosingTheClaimRace(t *testing.T) {
	store := &fakeStore{}
	store.onClaim = func() error {
		// The winner is visible by the time the conflict is reported.
		store.held = []application.Line{{ProductSKU: "SKU-A", Quantity: 2}}

		return application.ErrReservationAlreadyClaimed
	}

	require.NoError(t, useCase(store).Execute(context.Background(), testCommand()))
	assert.Equal(t, 2, store.finds, "the loser must re-read what the winner holds")
}

func TestReserveStockRejectsAnInvalidCommandBeforeTouchingTheStore(t *testing.T) {
	store := &fakeStore{}

	err := useCase(store).Execute(context.Background(), application.ReserveStockCommand{
		OrderID: "not-a-uuid",
		Lines:   []application.Line{{ProductSKU: "SKU-A", Quantity: 1}},
	})

	require.ErrorIs(t, err, application.ErrInvalidRequest)
	assert.Equal(t, 0, store.finds)
	assert.Equal(t, 0, store.claims)
}

// --- helpers -------------------------------------------------------------

func useCase(store application.ReservationStore) *application.ReserveStock {
	return application.NewReserveStock(store, zerolog.New(io.Discard))
}

type fakeStore struct {
	held     []application.Line
	claimErr error
	onClaim  func() error
	finds    int
	claims   int
}

var _ application.ReservationStore = (*fakeStore)(nil)

func (store *fakeStore) Find(context.Context, string) ([]application.Line, error) {
	store.finds++

	if len(store.held) == 0 {
		return nil, application.ErrReservationNotFound
	}

	return store.held, nil
}

func (store *fakeStore) Claim(_ context.Context, _ string, lines []application.Line) error {
	store.claims++

	if store.onClaim != nil {
		return store.onClaim()
	}

	if store.claimErr != nil {
		return store.claimErr
	}

	store.held = lines

	return nil
}
