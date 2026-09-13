//go:build integration

package amqpapi_test

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/inbound/amqpapi"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
)

// The queue arguments this consumer relies on — quorum queues, at-least-once
// dead-lettering, reject-publish overflow, and a queue TTL on a quorum queue — are
// all version-dependent. Asserting them against the broker version Compose pins is
// the difference between a documented guarantee and a hopeful one.
//
// If this fails after a broker upgrade, the fix is either to restore the settings or
// to weaken the claim in the docs. It must not be to delete the test.
func TestTopologyDeclaresOnThePinnedBrokerVersion(t *testing.T) {
	_, channel := startBroker(t)

	require.NoError(t, amqpapi.DeclareTopologyForTest(channel, orderAcceptedOnly(), amqpapi.TopologyConfig{
		RetryDelay: 30 * time.Second,
	}))

	// Declaring twice must be accepted. Recovery re-declares on every connect, so a
	// second declaration that disagreed with the first would kill the channel every
	// time the consumer reconnected.
	require.NoError(t, amqpapi.DeclareTopologyForTest(channel, orderAcceptedOnly(), amqpapi.TopologyConfig{
		RetryDelay: 30 * time.Second,
	}))
}

// A declaration whose arguments disagree with the existing queue must fail loudly.
// That is the property the shared exchange constants rely on: drift between two
// services' declarations shows up as a dead channel at startup, not as messages
// quietly going nowhere.
func TestConflictingRedeclarationIsRefused(t *testing.T) {
	_, channel := startBroker(t)

	require.NoError(t, amqpapi.DeclareTopologyForTest(channel, orderAcceptedOnly(), amqpapi.TopologyConfig{
		RetryDelay: 30 * time.Second,
	}))

	// A different TTL is a different queue definition.
	err := amqpapi.DeclareTopologyForTest(channel, orderAcceptedOnly(), amqpapi.TopologyConfig{
		RetryDelay: 60 * time.Second,
	})
	require.Error(t, err, "a changed queue argument must not be silently accepted")
	assert.Contains(t, err.Error(), "PRECONDITION_FAILED")
}

// The full retry leg, against a real broker: a rejected message leaves the working
// queue, waits out the TTL on the retry queue, and comes back. This is the loop the
// whole retry policy is built on, and none of it is this code's own behaviour.
func TestRejectedMessageReturnsViaTheRetryQueue(t *testing.T) {
	_, channel := startBroker(t)

	// Short TTL so the test does not wait 30 seconds for the mechanism it is
	// checking.
	require.NoError(t, amqpapi.DeclareTopologyForTest(channel, orderAcceptedOnly(), amqpapi.TopologyConfig{
		RetryDelay: time.Second,
	}))

	publish(t, channel, []byte("payload"))

	deliveries, err := channel.Consume(orderAcceptedQueue, "", false, false, false, false, nil)
	require.NoError(t, err)

	first := receive(t, deliveries, 10*time.Second)
	assert.Equal(t, []byte("payload"), first.Body)

	// requeue=false is the whole point: it dead-letters to the retry queue instead
	// of putting the message straight back at the head of this one.
	require.NoError(t, first.Nack(false, false))

	second := receive(t, deliveries, 20*time.Second)
	assert.Equal(t, []byte("payload"), second.Body, "the message must come back after the TTL")

	// x-death is how the round trip is counted. The consumer reads it for
	// observability, not as its retry budget.
	deaths, found := second.Headers["x-death"].([]any)
	require.True(t, found, "a redelivered message must carry x-death")
	require.NotEmpty(t, deaths)

	require.NoError(t, second.Ack(false))
}

func publish(t *testing.T, channel *amqp.Channel, body []byte) {
	t.Helper()

	err := channel.PublishWithContext(
		context.Background(),
		orderevents.ExchangeOrders,
		orderevents.RoutingKeyOrderAccepted,
		true,
		false,
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  orderevents.ContentTypeProtobuf,
			Type:         orderevents.MessageTypeOrderAccepted,
			Body:         body,
		},
	)
	require.NoError(t, err)
}

func receive(t *testing.T, deliveries <-chan amqp.Delivery, within time.Duration) amqp.Delivery {
	t.Helper()

	select {
	case delivery := <-deliveries:
		return delivery
	case <-time.After(within):
		t.Fatalf("no delivery arrived within %s", within)

		return amqp.Delivery{}
	}
}

// orderAcceptedOnly is the single subscription these topology tests declare.
func orderAcceptedOnly() []amqpapi.Subscription {
	return []amqpapi.Subscription{rawSubscription("order-accepted", nil)}
}

var orderAcceptedQueue = rawSubscription("order-accepted", nil).Queue()
