package application

import (
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

// OrderAcceptedEvent is the snapshot published when an order is accepted.
//
// Deliberately not the aggregate: a published event is a contract and the aggregate is
// not, EventID and OccurredAt are captured once so a retry announces the same fact, and
// an outbox will need a serialisable value the use case can write inside the order's
// transaction.
type OrderAcceptedEvent struct {
	EventID       string
	OrderID       domain.OrderID
	OccurredAt    time.Time
	CustomerEmail string
	Total         domain.Money

	// Lines carry no meaningful order.
	Lines []OrderAcceptedLine
}

type OrderAcceptedLine struct {
	ProductSKU string
	Quantity   int
	UnitPrice  domain.Money
}

// newOrderAcceptedEvent uses the order's updated-at rather than a fresh clock reading:
// the acceptance instant is already recorded on the order, and reading the clock again
// would timestamp the event after the fact it describes.
func newOrderAcceptedEvent(eventID string, order *domain.Order) OrderAcceptedEvent {
	orderItems := order.OrderItems()
	lines := make([]OrderAcceptedLine, 0, len(orderItems))
	for _, orderItem := range orderItems {
		lines = append(lines, OrderAcceptedLine{
			ProductSKU: orderItem.ProductSKU(),
			Quantity:   orderItem.Quantity(),
			UnitPrice:  orderItem.UnitPrice(),
		})
	}

	return OrderAcceptedEvent{
		EventID:       eventID,
		OrderID:       order.ID(),
		OccurredAt:    order.UpdatedAt(),
		CustomerEmail: order.CustomerEmail(),
		Total:         order.Total(),
		Lines:         lines,
	}
}
