package amqpapi

import (
	"context"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
)

type OrderAcceptedHandler interface {
	Execute(ctx context.Context, command application.OrderAcceptedCommand) application.Result
}

// This file is the entire per-event cost: a second event is another like it plus one
// line in wiring. Every value comes from the producer's contract rather than being
// retyped, so a consumer cannot drift from it silently.
func OrderAcceptedSubscription(handler OrderAcceptedHandler) Subscription {
	return Subscription{
		Name: "order-accepted",
		Source: ExchangeDeclaration{
			Name:       orderevents.ExchangeOrders,
			Kind:       orderevents.ExchangeOrdersKind,
			Durable:    orderevents.ExchangeOrdersDurable,
			AutoDelete: orderevents.ExchangeOrdersAutoDelete,
			Internal:   orderevents.ExchangeOrdersInternal,
		},
		RoutingKey:  orderevents.RoutingKeyOrderAccepted,
		MessageType: orderevents.MessageTypeOrderAccepted,
		ContentType: orderevents.ContentTypeProtobuf,

		Handle: func(
			ctx context.Context,
			messageID string,
			body []byte,
		) application.Result {
			command, err := decodeOrderAccepted(messageID, body)
			if err != nil {
				return application.Result{
					Outcome: application.OutcomeInvalid,
					Err:     err,
				}
			}

			return handler.Execute(ctx, command)
		},
	}
}
