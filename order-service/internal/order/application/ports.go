package application

import (
	"context"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type OrderRepository interface {
	Create(ctx context.Context, order *domain.Order) error
	Update(ctx context.Context, order *domain.Order, expectedStatus domain.Status) error

	// UpdateAndRecordEvent stores the order's new state and the event announcing it
	// in one transaction, so neither can exist without the other.
	UpdateAndRecordEvent(
		ctx context.Context,
		order *domain.Order,
		expectedStatus domain.Status,
		event OrderAcceptedEvent,
	) error

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

// OrderEventPublisher announces facts about orders. It is deliberately not
// called a notifier: this service does not know that anything sends email, and
// naming the port after one of its subscribers would put that knowledge back.
//
// The correlation id is passed explicitly because publication happens in the
// background, long after the request that accepted the order has finished.
type OrderEventPublisher interface {
	PublishOrderAccepted(ctx context.Context, event OrderAcceptedEvent, correlationID string) error
}

// OutboxEntry is one claimed outbox row.
type OutboxEntry struct {
	// EventID is set even when Event could not be read from storage.
	EventID       string
	Event         OrderAcceptedEvent
	CorrelationID string

	// Attempts counts the failed publications that used up retry budget.
	Attempts   int
	ClaimToken string
}

// EventOutbox is the store of events waiting to be published.
//
// Every write that takes an entry carries its claim token and changes nothing
// once another worker has claimed the row. MarkPublished is the exception, see
// the adapter.
type EventOutbox interface {
	// ClaimNext returns ErrOutboxEmpty when nothing is due. When the stored event
	// cannot be read, it returns the entry with ErrEventUnpublishable so the row
	// can be set aside.
	ClaimNext(ctx context.Context, lease time.Duration) (OutboxEntry, error)
	MarkPublished(ctx context.Context, eventID string) error

	// Release makes the row due again after delay without using up budget.
	Release(ctx context.Context, entry OutboxEntry, delay time.Duration) error

	// ScheduleRetry uses up one attempt and makes the row due again after delay.
	ScheduleRetry(ctx context.Context, entry OutboxEntry, delay time.Duration, reason string) error

	// MarkNeedsAttention stops automatic retries. countAttempt is false for a
	// permanent failure, which never reached the broker.
	MarkNeedsAttention(ctx context.Context, entry OutboxEntry, countAttempt bool, reason string) error
}

type Clock func() time.Time

type IDGenerator func() string
