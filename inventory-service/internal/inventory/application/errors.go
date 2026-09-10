package application

import "errors"

var (
	// ErrInvalidRequest covers everything the caller can fix by sending a
	// different request: a malformed order id, no lines, a blank SKU, or a
	// quantity outside the supported range.
	ErrInvalidRequest = errors.New("invalid reservation request")

	// ErrInsufficientStock means inventory answered, and the answer was no.
	ErrInsufficientStock = errors.New("insufficient stock")

	// ErrUnknownProductSKU means the request named a product this service has
	// never heard of, which is different from one that is merely sold out.
	ErrUnknownProductSKU = errors.New("unknown product sku")

	// ErrIdempotencyConflict means the order id was already used for a
	// different set of lines. Returning the original reservation would confirm
	// stock for items nobody reserved, so the request is refused instead.
	ErrIdempotencyConflict = errors.New("order id already used with different lines")

	// ErrReservationNotFound is returned by the store when an order id holds no
	// stock. It never reaches a caller.
	ErrReservationNotFound = errors.New("reservation not found")

	// ErrReservationAlreadyClaimed is returned by the store when a concurrent
	// request claimed the order id first. The use case re-reads rather than
	// failing.
	ErrReservationAlreadyClaimed = errors.New("reservation already claimed")
)
