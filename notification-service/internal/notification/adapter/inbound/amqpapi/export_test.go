package amqpapi

import amqp "github.com/rabbitmq/amqp091-go"

// Exported only in test builds: declaring the topology is something the consumer does
// for itself, not a service this package offers.
func DeclareTopologyForTest(
	channel *amqp.Channel,
	subscriptions []Subscription,
	config TopologyConfig,
) error {
	return declareTopology(channel, subscriptions, config)
}
