//go:build integration

package amqpapi_test

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/inbound/amqpapi"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	orderv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/gen/order/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
)

// This file tests the one thing the consumer is responsible for: turning an outcome
// into an AMQP action. The budget itself lives in the database and is tested against
// Postgres; here the handler is a stand-in that returns whatever outcome the case
// needs, so each branch can be reached deliberately.

func TestASentOutcomeAcknowledgesTheDelivery(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{application.OutcomeSent}}

	runConsumer(t, url, channel, handler)
	publishEvent(t, channel, validEventBody(t), "event-1")

	handler.waitForCalls(t, 1)

	// Acknowledged, so it is gone rather than waiting to be redelivered.
	assertQueueEmpties(t, channel, subscriptionUnderTest.Queue())
	assertQueueDepth(t, channel, subscriptionUnderTest.ParkedQueue(), 0)
}

func TestAnAlreadySentOutcomeAcknowledgesTheDelivery(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{application.OutcomeAlreadySent}}

	runConsumer(t, url, channel, handler)
	publishEvent(t, channel, validEventBody(t), "event-1")

	handler.waitForCalls(t, 1)

	assertQueueEmpties(t, channel, subscriptionUnderTest.Queue())
	assertQueueDepth(t, channel, subscriptionUnderTest.ParkedQueue(), 0)
}

// A transient failure goes round the retry loop and comes back, rather than spinning
// in place. The delay is produced by the message expiring on the retry queue, which is
// why the handler is called a second time only after the TTL.
func TestATransientFailureIsRetriedViaTheRetryQueue(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{
		application.OutcomeTransientFailure,
		application.OutcomeSent,
	}}

	runConsumer(t, url, channel, handler)
	publishEvent(t, channel, validEventBody(t), "event-1")

	handler.waitForCalls(t, 2)
	assertQueueEmpties(t, channel, subscriptionUnderTest.Queue())
	assertQueueDepth(t, channel, subscriptionUnderTest.ParkedQueue(), 0)
}

// A busy claim also goes back for another try. It must not be parked: nothing failed,
// another worker simply owns the effect right now.
func TestABusyClaimIsRetried(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{
		application.OutcomeClaimBusy,
		application.OutcomeAlreadySent,
	}}

	runConsumer(t, url, channel, handler)
	publishEvent(t, channel, validEventBody(t), "event-1")

	handler.waitForCalls(t, 2)

	assertQueueDepth(t, channel, subscriptionUnderTest.ParkedQueue(), 0)
}

// The end of the line. The message is parked with its body and properties intact, so
// somebody can look at it and republish it once the cause is fixed.
func TestAnExhaustedOutcomeParksTheMessage(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{application.OutcomeExhausted}}

	runConsumer(t, url, channel, handler)

	body := validEventBody(t)
	publishEvent(t, channel, body, "event-1")

	handler.waitForCalls(t, 1)

	parked := consumeOne(t, channel, subscriptionUnderTest.ParkedQueue(), 20*time.Second)

	// The original message, unaltered: the reason goes in a header rather than into
	// the body, which would corrupt the thing an operator needs to read.
	assert.Equal(t, body, parked.Body)
	assert.Equal(t, "event-1", parked.MessageId)
	assert.Equal(t, orderevents.ContentTypeProtobuf, parked.ContentType)
	assert.Equal(t, orderevents.MessageTypeOrderAccepted, parked.Type)
	assert.Equal(t, "request-1", parked.CorrelationId, "the correlation id must survive parking")
	assert.Equal(t, "attempts_exhausted", parked.Headers["x-parked-reason"])
	assert.Equal(t, subscriptionUnderTest.Queue(), parked.Headers["x-parked-from-queue"])
	assert.NotEmpty(t, parked.Headers["x-parked-at"])

	// And the working queue is empty: parked, not duplicated.
	assertQueueEmpties(t, channel, subscriptionUnderTest.Queue())
}

// A message that cannot be decoded is parked on its first delivery. Retrying it would
// fail identically three times, wasting the budget and delaying the alert — and the
// handler must never see it.
func TestAnUndecodableMessageIsParkedWithoutReachingTheHandler(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{application.OutcomeSent}}

	runConsumer(t, url, channel, handler)
	publishEvent(t, channel, []byte("this is not protobuf"), "event-1")

	parked := consumeOne(t, channel, subscriptionUnderTest.ParkedQueue(), 20*time.Second)
	assert.Equal(t, "invalid_event", parked.Headers["x-parked-reason"])
	assert.Equal(t, 0, handler.callCount(), "an invalid message must not reach the use case")
}

// An event that decodes but cannot be acted on — proto3 has no `required`, so this is
// a perfectly well-formed message with an empty customer email.
func TestAnEventMissingRequiredFieldsIsParked(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{application.OutcomeSent}}

	runConsumer(t, url, channel, handler)

	event := validEvent()
	event.CustomerEmail = ""
	body, err := proto.Marshal(event)
	require.NoError(t, err)

	publishEvent(t, channel, body, event.GetEventId())

	parked := consumeOne(t, channel, subscriptionUnderTest.ParkedQueue(), 20*time.Second)
	assert.Equal(t, "invalid_event", parked.Headers["x-parked-reason"])
	assert.Equal(t, 0, handler.callCount())
}

func runConsumer(
	t *testing.T,
	url string,
	channel *amqp.Channel,
	handler amqpapi.OrderAcceptedHandler,
) {
	t.Helper()

	runConsumerLogging(t, url, channel, handler, io.Discard)
}

func runConsumerLogging(
	t *testing.T,
	url string,
	channel *amqp.Channel,
	handler amqpapi.OrderAcceptedHandler,
	output io.Writer,
) {
	t.Helper()

	subscriptions := []amqpapi.Subscription{testSubscription(handler)}

	// Declared up front so the queues exist before anything publishes. Publishing into
	// an exchange with no binding drops the message, which would make these tests
	// flaky for a reason that has nothing to do with what they check.
	require.NoError(t, amqpapi.DeclareTopologyForTest(channel, subscriptions, amqpapi.TopologyConfig{
		RetryDelay: testRetryDelay,
	}))

	consumer, err := amqpapi.NewConsumer(
		amqpapi.Config{
			URL:            url,
			PrefetchCount:  1,
			ReconnectDelay: 200 * time.Millisecond,
			RetryDelay:     testRetryDelay,
			PublishTimeout: 5 * time.Second,
			HandleTimeout:  5 * time.Second,
		},
		subscriptions,
		zerolog.New(output),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = consumer.Run(ctx)
		close(done)
	}()

	t.Cleanup(func() {
		cancel()
		consumer.Wait()
		<-done
	})
}

func publishEvent(t *testing.T, channel *amqp.Channel, body []byte, eventID string) {
	t.Helper()

	err := channel.PublishWithContext(
		context.Background(),
		orderevents.ExchangeOrders,
		orderevents.RoutingKeyOrderAccepted,
		true,
		false,
		amqp.Publishing{
			DeliveryMode:  amqp.Persistent,
			ContentType:   orderevents.ContentTypeProtobuf,
			Type:          orderevents.MessageTypeOrderAccepted,
			MessageId:     eventID,
			CorrelationId: "request-1",
			Body:          body,
		},
	)
	require.NoError(t, err)
}

func validEvent() *orderv1.OrderAccepted {
	return &orderv1.OrderAccepted{
		EventId:       "event-1",
		OrderId:       "order-1",
		OccurredAt:    timestamppb.New(time.Date(2026, time.September, 13, 10, 0, 0, 0, time.UTC)),
		CustomerEmail: "buyer@example.test",
		TotalAmount:   &orderv1.Money{AmountInMinorUnits: 3000, Currency: "EUR"},
		Lines: []*orderv1.OrderLine{{
			ProductSku: "SKU-A",
			Quantity:   2,
			UnitPrice:  &orderv1.Money{AmountInMinorUnits: 1500, Currency: "EUR"},
		}},
	}
}

func validEventBody(t *testing.T) []byte {
	t.Helper()

	body, err := proto.Marshal(validEvent())
	require.NoError(t, err)

	return body
}

// fakeHandler returns a scripted outcome per call, so each branch of the consumer's
// settlement switch can be reached without needing a real database or provider.
type fakeHandler struct {
	mutex    sync.Mutex
	outcomes []application.Outcome
	calls    int
}

var _ amqpapi.OrderAcceptedHandler = (*fakeHandler)(nil)

func (handler *fakeHandler) Execute(
	_ context.Context,
	_ application.OrderAcceptedCommand,
) application.Result {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()

	index := handler.calls
	handler.calls++

	if index >= len(handler.outcomes) {
		// Past the script: succeed, so an unexpected extra delivery does not spin.
		return application.Result{Outcome: application.OutcomeSent}
	}

	return application.Result{Outcome: handler.outcomes[index], Attempts: index + 1}
}

func (handler *fakeHandler) callCount() int {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()

	return handler.calls
}

func (handler *fakeHandler) waitForCalls(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if handler.callCount() >= want {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("the handler was called %d times, want %d", handler.callCount(), want)
}

// The correlation id has to reach the log line, and putting it in a context.Context is
// not enough to do that — a context value is invisible to zerolog. The consumer builds a
// child logger carrying it explicitly, and this is what stops that being undone by a
// refactor that "tidies up" the extra logger.
//
// It is the thread from a customer's order to the line explaining what happened to their
// confirmation, and by the time the consumer runs the customer is long gone, so there is
// nothing else to correlate on.
func TestTheRequestIdReachesTheLogLines(t *testing.T) {
	url, channel := startBroker(t)
	handler := &fakeHandler{outcomes: []application.Outcome{application.OutcomeSent}}

	var logs safeBuffer
	runConsumerLogging(t, url, channel, handler, &logs)
	publishEvent(t, channel, validEventBody(t), "event-1")

	handler.waitForCalls(t, 1)
	assertQueueEmpties(t, channel, subscriptionUnderTest.Queue())

	assert.Contains(t, logs.String(), `"request_id":"request-1"`)
}

// zerolog writes from the consumer goroutine while the test reads from its own.
type safeBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.buffer.Write(p)
}

func (b *safeBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	return b.buffer.String()
}

// subscriptionUnderTest names the queues these tests inspect, derived exactly as the
// consumer derives them.
var subscriptionUnderTest = amqpapi.Subscription{Name: "order-accepted"}
