// Package notificationlog is a TEMPORARY stand-in for publishing OrderCreated.
// It records the notification that would have been sent and always succeeds.
//
// It is replaced by the transactional outbox: the order row and an outbox row
// are written in one transaction, and a relay publishes to RabbitMQ. This
// package exists so the create-order flow is complete before that lands, and
// deliberately calls out that a synchronous notification is NOT the documented
// architecture.
package notificationlog

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type Notifier struct {
	logger zerolog.Logger
}

var _ application.OrderNotifier = (*Notifier)(nil)

func NewNotifier(logger zerolog.Logger) *Notifier {
	return &Notifier{logger: logger}
}

func (notifier *Notifier) NotifyOrderCreated(ctx context.Context, order *domain.Order) error {
	notifier.logger.Info().
		Str("order_id", string(order.ID())).
		Str("customer_email", order.CustomerEmail()).
		Int64("total_amount_in_cents", order.Total().AmountInCents).
		Str("currency", order.Total().Currency).
		Msg("order created notification recorded by the notification stub")

	return nil
}
