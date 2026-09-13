//go:build integration

package rabbitmq_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	rabbitmqcontainer "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/rabbitmq"
)

const (
	testExchange   = "test.orders"
	testRoutingKey = "test.order.accepted.v1"
	testQueue      = "test.order-accepted"
)

// The whole point of this package is telling routed, not-routed and unknown
// apart against a real broker. None of it can be verified with a fake: the
// return-before-confirm ordering, the channel dying mid-wait, and the rebuild
// after that are all behaviours of RabbitMQ rather than of this code.

func TestPublishRoutedMessageCarriesTheWholeEnvelope(t *testing.T) {
	url := startBroker(t)
	publisher := newPublisher(t, url, 5*time.Second)

	channel := connect(t, url)
	bindQueue(t, channel, testRoutingKey)
	deliveries, err := channel.Consume(testQueue, "", true, false, false, false, nil)
	require.NoError(t, err)

	err = publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:      testExchange,
		RoutingKey:    testRoutingKey,
		Type:          "test.order.accepted.v1",
		ContentType:   "application/protobuf",
		MessageID:     "event-1",
		CorrelationID: "request-1",
		Body:          []byte("payload"),
	})
	require.NoError(t, err, "a bound queue exists, so this must be routed")

	select {
	case delivery := <-deliveries:
		assert.Equal(t, []byte("payload"), delivery.Body)
		assert.Equal(t, "event-1", delivery.MessageId)
		assert.Equal(t, "request-1", delivery.CorrelationId)
		assert.Equal(t, "test.order.accepted.v1", delivery.Type)
		assert.Equal(t, "application/protobuf", delivery.ContentType)
		assert.Equal(t, testRoutingKey, delivery.RoutingKey)

		// Persistent, so a broker restart does not drop an accepted order's
		// event. Set by the publisher rather than by the caller, because there is
		// no case in this system where a lost event is preferable to a slow one.
		assert.Equal(t, uint8(amqp.Persistent), delivery.DeliveryMode)
	case <-time.After(10 * time.Second):
		t.Fatal("the message never arrived")
	}
}

// The case a publisher confirm alone cannot catch. The broker accepts the
// message, finds no queue bound to the routing key, returns it, and then
// acknowledges it — so reading the ack would report success for a message that
// reached nobody.
func TestPublishReportsAnUnroutableMessage(t *testing.T) {
	url := startBroker(t)
	publisher := newPublisher(t, url, 5*time.Second)

	channel := connect(t, url)
	// A queue bound to a different key, so the exchange has bindings but none
	// that match. This is the shape of the real failure: a consumer exists but
	// was deployed with a stale routing key.
	bindQueue(t, channel, "test.order.accepted.v0")

	err := publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
		Body:       []byte("payload"),
	})

	require.ErrorIs(t, err, rabbitmq.ErrUnroutable)
	assert.NotErrorIs(t, err, rabbitmq.ErrOutcomeUnknown,
		"an explicit return is a known outcome, not an ambiguous one")
}

func TestPublishReportsAnUnreachableBroker(t *testing.T) {
	// Port 1 is reserved and nothing listens on it.
	publisher := newPublisher(t, "amqp://guest:guest@127.0.0.1:1/", 5*time.Second)

	err := publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
		Body:       []byte("payload"),
	})

	require.ErrorIs(t, err, rabbitmq.ErrUnavailable)
	assert.NotErrorIs(t, err, rabbitmq.ErrOutcomeUnknown,
		"never having connected is a known outcome: nothing was sent")

	// Closing something that was never dialled must not panic.
	require.NoError(t, publisher.Close())
}

// A confirmation that does not arrive in time is the outcome that must never be
// reported as failure: the broker may have routed the message already, so
// claiming nothing was published would be a lie and retrying risks a duplicate.
func TestPublishReportsAnUnknownOutcomeWhenTheConfirmationTimesOut(t *testing.T) {
	url := startBroker(t)
	publisher := newPublisher(t, url, time.Nanosecond)

	channel := connect(t, url)
	bindQueue(t, channel, testRoutingKey)

	err := publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
		Body:       []byte("payload"),
	})

	require.ErrorIs(t, err, rabbitmq.ErrOutcomeUnknown)
	assert.NotErrorIs(t, err, rabbitmq.ErrUnroutable)
	assert.NotErrorIs(t, err, rabbitmq.ErrUnavailable)
}

// Recovery has to rebuild the channel, confirm mode, exchange declarations and
// the return listener together. Publishing to an exchange that does not exist
// makes the broker kill the channel, which is the same damage a dropped
// connection does — so if the next publication succeeds, the rebuild works.
//
// Without this, a reconnected channel could silently lack its return listener
// and stop detecting unroutable messages, which is exactly the bug this package
// exists to prevent.
func TestPublishRebuildsAfterTheChannelDies(t *testing.T) {
	url := startBroker(t)
	publisher := newPublisher(t, url, 5*time.Second)

	channel := connect(t, url)
	bindQueue(t, channel, testRoutingKey)
	deliveries, err := channel.Consume(testQueue, "", true, false, false, false, nil)
	require.NoError(t, err)

	// Kills the channel: RabbitMQ answers a publish to an unknown exchange with
	// a NOT_FOUND channel exception.
	err = publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:   "test.exchange.that.does.not.exist",
		RoutingKey: testRoutingKey,
		Body:       []byte("doomed"),
	})
	require.Error(t, err, "publishing to a missing exchange must not look like success")

	err = publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:   testExchange,
		RoutingKey: testRoutingKey,
		MessageID:  "after-recovery",
		Body:       []byte("payload"),
	})
	require.NoError(t, err, "the publisher must rebuild its channel and carry on")

	select {
	case delivery := <-deliveries:
		assert.Equal(t, "after-recovery", delivery.MessageId)
	case <-time.After(10 * time.Second):
		t.Fatal("the message published after recovery never arrived")
	}

	// And the rebuilt channel still detects unroutable messages, which is the
	// part a socket-only reconnect would quietly lose.
	err = publisher.Publish(context.Background(), rabbitmq.Message{
		Exchange:   testExchange,
		RoutingKey: "test.order.accepted.unbound",
		Body:       []byte("payload"),
	})
	assert.ErrorIs(t, err, rabbitmq.ErrUnroutable)
}

func startBroker(t *testing.T) string {
	t.Helper()

	ctx := context.Background()

	container, err := rabbitmqcontainer.Run(ctx, "rabbitmq:4.1.4-alpine")
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	url, err := container.AmqpURL(ctx)
	require.NoError(t, err)

	return url
}

func newPublisher(t *testing.T, url string, publishTimeout time.Duration) *rabbitmq.Publisher {
	t.Helper()

	publisher := rabbitmq.NewPublisher(
		rabbitmq.Config{
			URL:            url,
			PublishTimeout: publishTimeout,
			// Short, so a test that deliberately breaks the channel is not made
			// to wait out a production-sized backoff before the rebuild.
			ReconnectDelay: time.Millisecond,
			Exchanges: []rabbitmq.ExchangeDeclaration{{
				Name:    testExchange,
				Kind:    "topic",
				Durable: true,
			}},
		},
		zerolog.New(io.Discard),
	)
	t.Cleanup(func() { _ = publisher.Close() })

	return publisher
}

func connect(t *testing.T, url string) *amqp.Channel {
	t.Helper()

	connection, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	channel, err := connection.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { _ = channel.Close() })

	err = channel.ExchangeDeclare(testExchange, "topic", true, false, false, false, nil)
	require.NoError(t, err)

	return channel
}

func bindQueue(t *testing.T, channel *amqp.Channel, routingKey string) {
	t.Helper()

	_, err := channel.QueueDeclare(testQueue, true, false, false, false, nil)
	require.NoError(t, err)

	require.NoError(t, channel.QueueBind(testQueue, routingKey, testExchange, false, nil))
}
