package amqpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
)

const queueNamespace = "notification"

// ExchangeDeclaration is a producer's exchange, as this consumer declares it so it can
// bind before that producer has started. Declaring is not owning: the values come from
// the producer's contract, and RabbitMQ fails the channel when two declarations
// disagree.
type ExchangeDeclaration struct {
	Name       string
	Kind       string
	Durable    bool
	AutoDelete bool
	Internal   bool
}

// Subscription is one event this service consumes. Everything that differs between
// events lives here; the retry loop, parked queue, budget and settlement live in the
// consumer and are shared, so a second event is a value and one line in wiring.
type Subscription struct {
	// Name derives all three queue names.
	Name string

	Source      ExchangeDeclaration
	RoutingKey  string
	MessageType string
	ContentType string

	// Handle is a function rather than an interface method because each event decodes
	// to its own type: keeping the decode inside the closure lets it stay type-safe
	// while the consumer stays ignorant of every event type. A decode failure is
	// reported as OutcomeInvalid so the consumer can park it without knowing why.
	Handle func(ctx context.Context, messageID string, body []byte) application.Result
}

func (subscription Subscription) Queue() string {
	return queueNamespace + "." + subscription.Name
}

// RetryQueue deliberately has no consumer: the TTL expiring is what sends the message
// back, and that wait is the point.
func (subscription Subscription) RetryQueue() string {
	return subscription.Queue() + ".retry"
}

func (subscription Subscription) ParkedQueue() string {
	return subscription.Queue() + ".parked"
}

// internalRoutingKey is used inside this service's private retry and parked exchanges,
// so nothing outside has to agree about it.
func (subscription Subscription) internalRoutingKey() string {
	return subscription.Name
}

// Validate is called at wiring time because every field here ends up in a queue name, a
// binding or an envelope check, and an empty one fails as "no messages" rather than as
// an error.
func (subscription Subscription) Validate() error {
	missing := make([]string, 0, 6)

	for field, value := range map[string]string{
		"Name":        subscription.Name,
		"Source.Name": subscription.Source.Name,
		"Source.Kind": subscription.Source.Kind,
		"RoutingKey":  subscription.RoutingKey,
		"MessageType": subscription.MessageType,
		"ContentType": subscription.ContentType,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, field)
		}
	}

	if subscription.Handle == nil {
		missing = append(missing, "Handle")
	}

	if len(missing) > 0 {
		return fmt.Errorf(
			"subscription %q is missing: %s",
			subscription.Name,
			strings.Join(missing, ", "),
		)
	}

	return nil
}
