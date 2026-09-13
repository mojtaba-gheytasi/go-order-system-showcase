// Package ordereventamqp publishes this service's order events to RabbitMQ.
package ordereventamqp

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	orderv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/gen/order/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/correlation"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/rabbitmq"
)

// MessagePublisher is the transport this adapter needs. Declared here, where it
// is used, so the platform package does not have to know an order exists.
type MessagePublisher interface {
	Publish(ctx context.Context, message rabbitmq.Message) error
}

type Publisher struct {
	messages MessagePublisher
}

var _ application.OrderEventPublisher = (*Publisher)(nil)

func NewPublisher(messages MessagePublisher) *Publisher {
	return &Publisher{messages: messages}
}

func (publisher *Publisher) PublishOrderAccepted(
	ctx context.Context,
	event application.OrderAcceptedEvent,
) error {
	body, err := proto.Marshal(protoOrderAccepted(event))
	if err != nil {
		return fmt.Errorf("marshal order accepted event: %w", err)
	}

	// Detached from the request's cancellation, deliberately. The order is
	// already durably accepted; a client that hangs up must not cancel the
	// announcement of a fact that has already happened. The publish timeout still
	// bounds it, so this cannot hang.
	//
	// WithoutCancel rather than a fresh context, because the request id lives in
	// there and is what ties this publication to the order that caused it.
	publishCtx := context.WithoutCancel(ctx)

	return publisher.messages.Publish(publishCtx, rabbitmq.Message{
		Exchange:      orderevents.ExchangeOrders,
		RoutingKey:    orderevents.RoutingKeyOrderAccepted,
		Type:          orderevents.MessageTypeOrderAccepted,
		ContentType:   orderevents.ContentTypeProtobuf,
		MessageID:     event.EventID,
		CorrelationID: correlation.FromContext(ctx),
		Body:          body,
	})
}

// protoOrderAccepted maps the application's snapshot onto the wire type.
//
// The two are separate on purpose: the snapshot is this service's own vocabulary
// and the protobuf message is the published contract, so the mapping is the one
// place that has to change when either moves. The field names differ in one place
// for the same reason — the domain says cents, while the contract says minor
// units, which is the more general statement a consumer can rely on for any
// currency.
func protoOrderAccepted(event application.OrderAcceptedEvent) *orderv1.OrderAccepted {
	lines := make([]*orderv1.OrderLine, 0, len(event.Lines))
	for _, line := range event.Lines {
		lines = append(lines, &orderv1.OrderLine{
			ProductSku: line.ProductSKU,
			Quantity:   int32(line.Quantity),
			UnitPrice:  protoMoney(line.UnitPrice.AmountInCents, line.UnitPrice.Currency),
		})
	}

	return &orderv1.OrderAccepted{
		EventId:       event.EventID,
		OrderId:       string(event.OrderID),
		OccurredAt:    timestamppb.New(event.OccurredAt),
		CustomerEmail: event.CustomerEmail,
		TotalAmount:   protoMoney(event.Total.AmountInCents, event.Total.Currency),
		Lines:         lines,
	}
}

func protoMoney(amountInMinorUnits int64, currency string) *orderv1.Money {
	return &orderv1.Money{
		AmountInMinorUnits: amountInMinorUnits,
		Currency:           currency,
	}
}
