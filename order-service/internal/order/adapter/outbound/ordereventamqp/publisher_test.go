package ordereventamqp_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	orderv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/gen/order/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/ordereventamqp"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/rabbitmq"
)

var occurredAt = time.Date(2026, time.September, 13, 10, 30, 0, 0, time.UTC)

func TestPublishOrderAcceptedSetsTheEnvelope(t *testing.T) {
	messages := &fakeMessagePublisher{}

	err := ordereventamqp.NewPublisher(messages).
		PublishOrderAccepted(context.Background(), testEvent(), "request-7")
	require.NoError(t, err)

	require.Len(t, messages.published, 1)
	message := messages.published[0]

	assert.Equal(t, orderevents.ExchangeOrders, message.Exchange)
	assert.Equal(t, orderevents.RoutingKeyOrderAccepted, message.RoutingKey)
	assert.Equal(t, orderevents.MessageTypeOrderAccepted, message.Type)

	// Spelled out rather than compared to the constant. The constant is what the
	// code uses, so comparing against it would agree with any value; the point
	// here is that the value on the wire is the registered type and not the
	// pre-standard "application/x-protobuf".
	assert.Equal(t, "application/protobuf", message.ContentType)

	// The event id, not the order id: the message identifies this publication.
	assert.Equal(t, "event-1", message.MessageID)

	// The standard AMQP property rather than a custom header — the transport
	// already has a field for this, and it is what ties a consumer's log line to
	// the request that caused the order.
	assert.Equal(t, "request-7", message.CorrelationID)
}

func TestPublishOrderAcceptedEncodesEveryField(t *testing.T) {
	messages := &fakeMessagePublisher{}

	err := ordereventamqp.NewPublisher(messages).
		PublishOrderAccepted(context.Background(), testEvent(), "request-7")
	require.NoError(t, err)

	require.Len(t, messages.published, 1)

	var decoded orderv1.OrderAccepted
	require.NoError(t, proto.Unmarshal(messages.published[0].Body, &decoded))

	assert.Equal(t, "event-1", decoded.GetEventId())
	assert.Equal(t, "order-1", decoded.GetOrderId())
	assert.Equal(t, occurredAt, decoded.GetOccurredAt().AsTime())
	assert.Equal(t, "buyer@example.test", decoded.GetCustomerEmail())

	// The domain calls this amount cents; the contract calls it minor units,
	// which is the more general claim. The mapping is the only place that knows.
	assert.Equal(t, int64(3000), decoded.GetTotalAmount().GetAmountInMinorUnits())
	assert.Equal(t, "EUR", decoded.GetTotalAmount().GetCurrency())

	require.Len(t, decoded.GetLines(), 2)
	assert.Equal(t, "SKU-A", decoded.GetLines()[0].GetProductSku())
	assert.Equal(t, int32(2), decoded.GetLines()[0].GetQuantity())
	assert.Equal(t, int64(1250), decoded.GetLines()[0].GetUnitPrice().GetAmountInMinorUnits())
	assert.Equal(t, "SKU-B", decoded.GetLines()[1].GetProductSku())
	assert.Equal(t, int32(1), decoded.GetLines()[1].GetQuantity())
}

func TestPublishOrderAcceptedWithoutARequestIDSendsNoCorrelation(t *testing.T) {
	messages := &fakeMessagePublisher{}

	err := ordereventamqp.NewPublisher(messages).
		PublishOrderAccepted(context.Background(), testEvent(), "")
	require.NoError(t, err)

	require.Len(t, messages.published, 1)
	assert.Empty(t, messages.published[0].CorrelationID)
}

// The worker's retry policy depends on which outcome happened, so the transport's
// verdict must survive, and the two it treats specially must be recognisable
// without the worker knowing about RabbitMQ.
func TestPublishOrderAcceptedReturnsTheTransportOutcome(t *testing.T) {
	tests := map[string]struct {
		transport   error
		application error
	}{
		"unavailable": {transport: rabbitmq.ErrUnavailable, application: application.ErrPublisherUnavailable},
		"unknown":     {transport: rabbitmq.ErrOutcomeUnknown, application: application.ErrPublicationOutcomeUnknown},
		"unroutable":  {transport: rabbitmq.ErrUnroutable},
		"rejected":    {transport: rabbitmq.ErrRejected},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			messages := &fakeMessagePublisher{err: test.transport}

			err := ordereventamqp.NewPublisher(messages).
				PublishOrderAccepted(context.Background(), testEvent(), "request-7")

			assert.ErrorIs(t, err, test.transport)
			if test.application != nil {
				assert.ErrorIs(t, err, test.application)
			}
			assert.NotErrorIs(t, err, application.ErrEventUnpublishable)
		})
	}
}

func testEvent() application.OrderAcceptedEvent {
	return application.OrderAcceptedEvent{
		EventID:       "event-1",
		OrderID:       "order-1",
		OccurredAt:    occurredAt,
		CustomerEmail: "buyer@example.test",
		Total:         domainMoney(3000),
		Lines: []application.OrderAcceptedLine{
			{ProductSKU: "SKU-A", Quantity: 2, UnitPrice: domainMoney(1250)},
			{ProductSKU: "SKU-B", Quantity: 1, UnitPrice: domainMoney(500)},
		},
	}
}

type fakeMessagePublisher struct {
	err       error
	published []rabbitmq.Message
}

var _ ordereventamqp.MessagePublisher = (*fakeMessagePublisher)(nil)

func (publisher *fakeMessagePublisher) Publish(
	_ context.Context,
	message rabbitmq.Message,
) error {
	publisher.published = append(publisher.published, message)

	if publisher.err != nil {
		// Wrapped, so the test checks that the sentinel survives the adapter
		// rather than that some error came back.
		return fmt.Errorf("publish: %w", publisher.err)
	}

	return nil
}

func domainMoney(amountInCents int64) domain.Money {
	return domain.Money{AmountInCents: amountInCents, Currency: "EUR"}
}
