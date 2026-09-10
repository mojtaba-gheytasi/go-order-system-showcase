package application

import "context"

type ReservationStore interface {
	// Find returns the products currently held for orderID, in canonical order,
	// or ErrReservationNotFound when the order holds nothing.
	Find(ctx context.Context, orderID string) ([]Line, error)

	// Claim holds stock for every line, atomically. Either all of it is held or
	// none of it is: a request that cannot be filled leaves stock untouched and
	// records nothing, so retrying it later is a fresh attempt.
	//
	// It reports ErrInsufficientStock or ErrUnknownProductSKU when the request
	// cannot be filled, and ErrReservationAlreadyClaimed when a concurrent
	// request took this order id first — in which case that request has already
	// committed and Find will see it.
	Claim(ctx context.Context, orderID string, lines []Line) error
}
