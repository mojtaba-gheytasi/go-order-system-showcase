package orderevents_test

import (
	"testing"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
)

// These assertions look tautological and are not. buf breaking guards the
// message schema, but a routing key is a Go string, so nothing in the protobuf
// toolchain notices when one changes. Renaming a key silently stops delivering
// to every consumer bound to the old one — the failure is an empty queue, not an
// error — so the literals are written down a second time here. Changing a key
// has to fail this test, which is the moment to ask whether it should instead be
// published as a new key alongside the old one.
//
// Standard library only, deliberately: this module's dependency list is part of
// its contract, and a test helper is not worth adding to it.
func TestTopologyNamesAreStable(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		actual   string
		expected string
	}{
		{"exchange", orderevents.ExchangeOrders, "orders"},
		{"exchange kind", orderevents.ExchangeOrdersKind, "topic"},
		{"routing key", orderevents.RoutingKeyOrderAccepted, "order.accepted.v1"},
		{"message type", orderevents.MessageTypeOrderAccepted, "order.accepted.v1"},
		{"content type", orderevents.ContentTypeProtobuf, "application/protobuf"},
	} {
		if testCase.actual != testCase.expected {
			t.Errorf("%s: got %q, want %q", testCase.name, testCase.actual, testCase.expected)
		}
	}
}

// The exchange must survive a broker restart and must not disappear with its
// last consumer. Both sides declare it, and RabbitMQ fails the channel when two
// declarations disagree, so these are the values that keep publisher and
// consumer able to start in either order.
func TestExchangeIsDurableAndPermanent(t *testing.T) {
	t.Parallel()

	if orderevents.ExchangeOrdersDurable == false {
		t.Error("exchange must be durable to survive a broker restart")
	}

	if orderevents.ExchangeOrdersAutoDelete {
		t.Error("exchange must not be auto-deleted when the last consumer disconnects")
	}

	if orderevents.ExchangeOrdersInternal {
		t.Error("exchange must not be internal; order-service publishes to it directly")
	}
}
