package amqpapi

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
)

// The three queue names are derived from one field, so a new event cannot end up with
// a working queue and a retry queue that disagree about which event they serve.
//
// The exact strings are asserted because they are live broker objects: changing one
// orphans a durable queue holding real messages, and renaming it is a migration rather
// than an edit.
func TestQueueNamesAreDerivedFromTheSubscriptionName(t *testing.T) {
	subscription := Subscription{Name: "order-accepted"}

	assert.Equal(t, "notification.order-accepted", subscription.Queue())
	assert.Equal(t, "notification.order-accepted.retry", subscription.RetryQueue())
	assert.Equal(t, "notification.order-accepted.parked", subscription.ParkedQueue())
	assert.Equal(t, "order-accepted", subscription.internalRoutingKey())
}

func TestASecondEventGetsItsOwnQueues(t *testing.T) {
	accepted := Subscription{Name: "order-accepted"}
	cancelled := Subscription{Name: "order-cancelled"}

	assert.NotEqual(t, accepted.Queue(), cancelled.Queue())
	assert.NotEqual(t, accepted.RetryQueue(), cancelled.RetryQueue())
	assert.NotEqual(t, accepted.ParkedQueue(), cancelled.ParkedQueue())
}

// Each of these produces a queue that silently receives nothing rather than an error,
// which is why they are refused at startup instead of being discovered in production
// as "the emails stopped".
func TestValidateRejectsAnIncompleteSubscription(t *testing.T) {
	for name, damage := range map[string]func(*Subscription){
		"no name":          func(s *Subscription) { s.Name = "" },
		"no exchange":      func(s *Subscription) { s.Source.Name = "" },
		"no exchange kind": func(s *Subscription) { s.Source.Kind = "" },
		"no routing key":   func(s *Subscription) { s.RoutingKey = "" },
		"no message type":  func(s *Subscription) { s.MessageType = "" },
		"no content type":  func(s *Subscription) { s.ContentType = "" },
		"no handler":       func(s *Subscription) { s.Handle = nil },
	} {
		t.Run(name, func(t *testing.T) {
			subscription := completeSubscription()
			damage(&subscription)

			require.Error(t, subscription.Validate())
		})
	}
}

func TestValidateAcceptsACompleteSubscription(t *testing.T) {
	require.NoError(t, completeSubscription().Validate())
}

// Two subscriptions sharing a name would share all three queues and consume each
// other's messages, which is silent rather than noisy — so it is refused at wiring.
func TestNewConsumerRejectsDuplicateNames(t *testing.T) {
	_, err := NewConsumer(
		Config{},
		[]Subscription{completeSubscription(), completeSubscription()},
		testLogger(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestNewConsumerRejectsNoSubscriptions(t *testing.T) {
	_, err := NewConsumer(Config{}, nil, testLogger())

	require.Error(t, err)
}

func TestNewConsumerRejectsAnInvalidSubscription(t *testing.T) {
	broken := completeSubscription()
	broken.RoutingKey = ""

	_, err := NewConsumer(Config{}, []Subscription{broken}, testLogger())

	require.Error(t, err)
}

func completeSubscription() Subscription {
	return Subscription{
		Name:        "order-accepted",
		Source:      ExchangeDeclaration{Name: "orders", Kind: "topic", Durable: true},
		RoutingKey:  "order.accepted.v1",
		MessageType: "order.accepted.v1",
		ContentType: "application/protobuf",
		Handle: func(context.Context, string, []byte) application.Result {
			return application.Result{Outcome: application.OutcomeSent}
		},
	}
}

func testLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}
