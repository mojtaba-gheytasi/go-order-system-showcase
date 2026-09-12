package application

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrOrderNotFound           = errors.New("order not found")
	ErrDuplicateIdempotencyKey = errors.New("duplicate idempotency key")
	ErrOrderStateConflict      = errors.New("order state conflict")
	ErrProductNotFound         = errors.New("product not found")
	ErrCatalogUnavailable      = errors.New("product catalog unavailable")

	ErrInsufficientStock = errors.New("insufficient stock")

	ErrInventoryUnavailable = errors.New("inventory unavailable")
)

// The errors below name what a caller has to change to succeed. Each unwraps to
// the sentinel above it, so existing errors.Is checks keep working and the
// detail is additive.
//
// They are this service's own types even where inventory has a counterpart.
// Neither service may reach into the other's internals; the protobuf contract is
// the only vocabulary they share, and each side translates at its own edge.

// Shortfall is one line the warehouse could not cover.
type Shortfall struct {
	ProductSKU string
	Requested  int32

	// Available is what was free when inventory refused the reservation.
	// Advisory: another order may take it moments later.
	Available int32
}

// InsufficientStockError carries every line that blocked the order, so a
// customer can fix a whole basket in one attempt.
//
// It is only available on the attempt that actually reached inventory. A repeat
// of an order already recorded as rejected is answered from this service's own
// database, which holds the verdict but not the shortfall behind it.
type InsufficientStockError struct {
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
