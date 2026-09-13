// Package orderevents holds the broker-level half of order-service's event contract:
// where its events are published and how a consumer recognises one.
//
// buf compares schemas and cannot see a changed Go string, so topic_test.go pins these
// values. Changing a routing key is not an edit here; it is a new key published
// alongside the old one until consumers have moved.
package orderevents

const ExchangeOrders = "orders"

// Both the publisher and its consumers declare this exchange, so that either can start
// first. RabbitMQ fails the channel with PRECONDITION_FAILED when two declarations
// disagree, which is what makes these values worth stating once.
const (
	ExchangeOrdersKind       = "topic"
	ExchangeOrdersDurable    = true
	ExchangeOrdersAutoDelete = false
	ExchangeOrdersInternal   = false
)

// The version is in the key so a consumer binds to the version it can decode.
const RoutingKeyOrderAccepted = "order.accepted.v1"

// MessageTypeOrderAccepted is what a consumer checks before decoding. It equals the
// routing key today and is separate because a queue bound with a wildcard receives keys
// it did not name individually — what the body claims to be is the trustworthy part.
const MessageTypeOrderAccepted = "order.accepted.v1"

// ContentTypeProtobuf is the registered type. The older "application/x-protobuf"
// spelling is not accepted: a consumer taking both would be guessing.
const ContentTypeProtobuf = "application/protobuf"
