package application

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/rs/zerolog"
)

// claimAttempts bounds the read-then-claim loop. Two is exact rather than a
// margin: the first pass may lose the race for an order id, and the second is
// guaranteed to see the winner, because Claim reports a conflict only once the
// winning transaction has committed. A third pass could only run if the store
// broke that contract, and it would fail identically one round trip later.
const claimAttempts = 2

type ReserveStockCommand struct {
	OrderID string
	Lines   []Line
}

type ReserveStock struct {
	reservations ReservationStore
	logger       zerolog.Logger
}

func NewReserveStock(reservations ReservationStore, logger zerolog.Logger) *ReserveStock {
	return &ReserveStock{reservations: reservations, logger: logger}
}

// Execute holds stock for an order, or explains why it cannot.
//
// The order id is the idempotency key, so repeating a call that already
// succeeded is accepted without holding the stock twice. A call that failed
// leaves nothing behind: retrying it is a fresh attempt, and will succeed if the
// warehouse has been restocked in the meantime.
func (useCase *ReserveStock) Execute(ctx context.Context, command ReserveStockCommand) error {
	orderID, lines, err := Canonicalize(command.OrderID, command.Lines)
	if err != nil {
		return err
	}

	for range claimAttempts {
		held, err := useCase.reservations.Find(ctx, orderID)
		if err == nil {
			return confirmSameProducts(held, lines)
		}
		if errors.Is(err, ErrReservationNotFound) == false {
			return fmt.Errorf("find reservation: %w", err)
		}

		err = useCase.reservations.Claim(ctx, orderID, lines)
		if err == nil {
			return nil
		}

		// A concurrent request took this order id first. It has already
		// committed by the time the conflict is reported, so the next Find is
		// guaranteed to see it.
		if errors.Is(err, ErrReservationAlreadyClaimed) {
			continue
		}

		return err
	}

	return fmt.Errorf(
		"claim reservation for order %s: gave up after %d attempts",
		orderID,
		claimAttempts,
	)
}

// confirmSameProducts accepts a repeat only when it asks for what is already
// held. The same order id carrying different products is a caller mistake, and
// answering success would confirm stock for items nobody reserved: the order
// would be accepted, and the warehouse would never pack it.
//
// Both sides travel with the refusal, because the difference between them is
// what tells a caller whether it resubmitted a changed basket or generated a
// colliding order id.
func confirmSameProducts(held, requested []Line) error {
	if slices.Equal(held, requested) == false {
		return &IdempotencyConflictError{Held: held, Requested: requested}
	}

	return nil
}
