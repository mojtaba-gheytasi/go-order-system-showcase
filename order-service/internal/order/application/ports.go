package application

import (
	"context"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type OrderRepository interface {
	Create(ctx context.Context, order *domain.Order) error
	Update(ctx context.Context, order *domain.Order, expectedStatus domain.Status) error
	FindByID(ctx context.Context, id domain.OrderID) (*domain.Order, error)
	FindByIdempotencyKey(ctx context.Context, key string) (*domain.Order, error)
}

type ReservationLine struct {
	ProductSKU string
	Quantity   int
}

type ReservationRequest struct {
	OrderID domain.OrderID
	Lines   []ReservationLine
}

// InventoryReserver treats OrderID as the operation's idempotency key, so a
// repeated request for the same OrderID does not hold the stock twice.
//
// There is nothing to return on success: inventory holds stock against the order
// id the caller already has, so success is the whole answer.
type InventoryReserver interface {
	Reserve(ctx context.Context, request ReservationRequest) error
}

type OrderNotifier interface {
	NotifyOrderCreated(ctx context.Context, order *domain.Order) error
}

type Clock func() time.Time

type IDGenerator func() string
