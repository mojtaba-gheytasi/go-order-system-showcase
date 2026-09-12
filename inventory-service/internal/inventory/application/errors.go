package application

import (
	"errors"
	"fmt"
	"strings"
)

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

// The errors below carry what the caller needs to act on a refusal. Each one
// unwraps to the sentinel above it, so every existing errors.Is check keeps
// working and the detail is purely additive: code that does not know about
// these types behaves exactly as it did before.
//
// They hold no protobuf types. Turning them into status details is the inbound
// adapter's job, which is what keeps the transport out of this package.

// FieldViolation names one problem with a request, against the field that
// carries it. Field is a path into the request message — "order_id",
// "lines[2].quantity" — and is zero-based, because it indexes the request as
// the caller sent it.
type FieldViolation struct {
	Field       string
	Description string
}

// InvalidRequestError reports every violation found, rather than the first, so
// a caller fixes a request in one round trip instead of one field per attempt.
type InvalidRequestError struct {
	Violations []FieldViolation

	// Omitted counts violations past the reporting cap. A request can be
	// malformed in as many places as it has lines, and an error response is not
	// the place to mirror that back in full.
	Omitted int
}

func (err *InvalidRequestError) Error() string {
	descriptions := make([]string, 0, len(err.Violations))
	for _, violation := range err.Violations {
		descriptions = append(
			descriptions,
			fmt.Sprintf("%s %s", violation.Field, violation.Description),
		)
	}

	message := fmt.Sprintf("%s: %s", ErrInvalidRequest, strings.Join(descriptions, "; "))
	if err.Omitted > 0 {
		message = fmt.Sprintf("%s (and %d more)", message, err.Omitted)
	}

	return message
}

func (err *InvalidRequestError) Unwrap() error { return ErrInvalidRequest }

// Shortfall is one line the warehouse could not cover.
type Shortfall struct {
	ProductSKU string
	Requested  int32

	// Available is what was free when the claim was refused. It is advisory:
	// another order may take it a moment later, and a restock may make more of
	// it.
	Available int32
}

// InsufficientStockError carries every line that could not be filled.
type InsufficientStockError struct {
	// Sorted by ProductSKU, so two reports of the same problem compare equal.
	Shortfalls []Shortfall
}

func (err *InsufficientStockError) Error() string {
	descriptions := make([]string, 0, len(err.Shortfalls))
	for _, shortfall := range err.Shortfalls {
		descriptions = append(descriptions, fmt.Sprintf(
			"%s (%d requested, %d available)",
			shortfall.ProductSKU,
			shortfall.Requested,
			shortfall.Available,
		))
	}

	return fmt.Sprintf("%s: %s", ErrInsufficientStock, strings.Join(descriptions, ", "))
}

func (err *InsufficientStockError) Unwrap() error { return ErrInsufficientStock }

// UnknownProductSKUError names every product the request asked for that this
// service does not stock.
type UnknownProductSKUError struct {
	// Sorted, for the same reason the shortfalls are.
	ProductSKUs []string
}

func (err *UnknownProductSKUError) Error() string {
	return fmt.Sprintf("%s: %s", ErrUnknownProductSKU, strings.Join(err.ProductSKUs, ", "))
}

func (err *UnknownProductSKUError) Unwrap() error { return ErrUnknownProductSKU }

// IdempotencyConflictError reports both sides of the conflict, because the
// difference between them is the diagnosis: it separates a resubmission under a
// changed basket from a colliding order id, and those have different fixes.
type IdempotencyConflictError struct {
	Held      []Line
	Requested []Line
}

func (err *IdempotencyConflictError) Error() string {
	return fmt.Sprintf(
		"%s: holds %s, requested %s",
		ErrIdempotencyConflict,
		describeLines(err.Held),
		describeLines(err.Requested),
	)
}

func (err *IdempotencyConflictError) Unwrap() error { return ErrIdempotencyConflict }

func describeLines(lines []Line) string {
	descriptions := make([]string, 0, len(lines))
	for _, line := range lines {
		descriptions = append(descriptions, fmt.Sprintf("%s x%d", line.ProductSKU, line.Quantity))
	}

	return "[" + strings.Join(descriptions, ", ") + "]"
}
