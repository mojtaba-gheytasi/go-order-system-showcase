package application

import "errors"

var (
	ErrOrderNotFound           = errors.New("order not found")
	ErrDuplicateIdempotencyKey = errors.New("duplicate idempotency key")
	ErrOrderStateConflict      = errors.New("order state conflict")
	ErrProductNotFound         = errors.New("product not found")
	ErrCatalogUnavailable      = errors.New("product catalog unavailable")

	ErrInsufficientStock = errors.New("insufficient stock")

	ErrInventoryUnavailable = errors.New("inventory unavailable")
)
