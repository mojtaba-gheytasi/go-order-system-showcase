//go:build integration

package amqpapi_test

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	rabbitmqcontainer "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

// testRetryDelay is short so tests exercise the retry loop rather than wait out the
// production delay. The mechanism under test is the round trip, not its length.
const testRetryDelay = time.Second

// brokerImage is the version Compose pins. The queue arguments this consumer uses —
// quorum queues, at-least-once dead-lettering, a TTL on a quorum queue — are
// version-dependent, so the tests must run against the same broker the system does.
const brokerImage = "rabbitmq:4.1.4-alpine"

// startBroker runs a broker for one test and returns its URL and a channel on it.
//
// Both are returned rather than stashed somewhere shared: a test that needs to point a
// consumer at the broker also needs to inspect it, and threading two values through is
// less surprising than a lookup that has to be called in the right order.
func startBroker(t *testing.T) (string, *amqp.Channel) {
	t.Helper()

	ctx := context.Background()

	container, err := rabbitmqcontainer.Run(ctx, brokerImage)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	url, err := container.AmqpURL(ctx)
	require.NoError(t, err)

	connection, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	channel, err := connection.Channel()
	require.NoError(t, err)

	return url, channel
}

// queueDepth reports how many messages are sitting in a queue.
//
// A passive declare rather than a management API call: it needs no HTTP client and no
// credentials, and it fails loudly if the queue does not exist, which is itself
// something a test would want to know.
func queueDepth(t *testing.T, channel *amqp.Channel, queue string) int {
	t.Helper()

	inspected, err := channel.QueueDeclarePassive(queue, true, false, false, false, nil)
	require.NoError(t, err)

	return inspected.Messages
}

func assertQueueDepth(t *testing.T, channel *amqp.Channel, queue string, expected int) {
	t.Helper()

	assert.Equal(t, expected, queueDepth(t, channel, queue), "depth of %s", queue)
}

// assertQueueEmpties waits for a queue to drain.
//
// Polled rather than asserted once: acknowledgement is asynchronous, so a queue that
// is about to be empty is not yet empty, and a bare assertion here would be flaky.
func assertQueueEmpties(t *testing.T, channel *amqp.Channel, queue string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if queueDepth(t, channel, queue) == 0 {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("%s still holds %d messages", queue, queueDepth(t, channel, queue))
}

// consumeOne takes a single message off a queue, failing if none arrives in time.
func consumeOne(
	t *testing.T,
	channel *amqp.Channel,
	queue string,
	within time.Duration,
) amqp.Delivery {
	t.Helper()

	deliveries, err := channel.Consume(queue, "", true, false, false, false, nil)
	require.NoError(t, err)

	select {
	case delivery := <-deliveries:
		return delivery
	case <-time.After(within):
		t.Fatalf("nothing arrived on %s within %s", queue, within)

		return amqp.Delivery{}
	}
}
