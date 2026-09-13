//go:build integration

package amqpapi_test

import (
	"context"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/inbound/amqpapi"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
)

// testSubscription is the real OrderAccepted subscription with its handler swapped for
// a scripted one, so these tests drive the same code path production does.
func testSubscription(handler amqpapi.OrderAcceptedHandler) amqpapi.Subscription {
	return amqpapi.OrderAcceptedSubscription(handler)
}

// rawSubscription skips decoding entirely, for tests about transport behaviour that do
// not care what the body says.
func rawSubscription(
	name string,
	handle func(ctx context.Context, messageID string, body []byte) application.Result,
) amqpapi.Subscription {
	return amqpapi.Subscription{
		Name: name,
		Source: amqpapi.ExchangeDeclaration{
			Name:       orderevents.ExchangeOrders,
			Kind:       orderevents.ExchangeOrdersKind,
			Durable:    orderevents.ExchangeOrdersDurable,
			AutoDelete: orderevents.ExchangeOrdersAutoDelete,
			Internal:   orderevents.ExchangeOrdersInternal,
		},
		RoutingKey:  orderevents.RoutingKeyOrderAccepted,
		MessageType: orderevents.MessageTypeOrderAccepted,
		ContentType: orderevents.ContentTypeProtobuf,
		Handle:      handle,
	}
}
