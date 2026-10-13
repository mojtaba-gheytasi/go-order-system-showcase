package rabbitmq_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/rabbitmq"
)

// A broker that accepts the connection and then says nothing must not hold a
// publication for the library's default 30 seconds.
func TestPublishGivesUpOnABrokerThatNeverCompletesTheHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = connection.Close() })
		}
	}()

	publisher := rabbitmq.NewPublisher(
		rabbitmq.Config{
			URL:            "amqp://guest:guest@" + listener.Addr().String() + "/",
			PublishTimeout: time.Second,
			DialTimeout:    200 * time.Millisecond,
			ReconnectDelay: time.Millisecond,
		},
		zerolog.New(io.Discard),
	)
	t.Cleanup(func() { _ = publisher.Close() })

	started := time.Now()
	err = publisher.Publish(context.Background(), rabbitmq.Message{Exchange: "orders"})

	require.ErrorIs(t, err, rabbitmq.ErrUnavailable)
	assert.Less(t, time.Since(started), 2*time.Second)
}
