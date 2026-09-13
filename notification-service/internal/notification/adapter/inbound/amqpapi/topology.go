package amqpapi

import (
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Named here and not in any producer's contract: a routing key and a message shape are
// the contract, but retry counts and dead-letter queues are this consumer's own business.
const (
	ExchangeRetry  = "notification.retry"
	ExchangeParked = "notification.parked"
)

type TopologyConfig struct {
	// RetryDelay is the TTL on the retry queue: the delay comes from the message
	// expiring there rather than from this process sleeping.
	RetryDelay time.Duration
}

// Idempotent and called on every connect: recovery must rebuild channel, prefetch,
// topology and subscriptions together, and one function doing all of it is how none
// gets forgotten.
func declareTopology(
	channel *amqp.Channel,
	subscriptions []Subscription,
	config TopologyConfig,
) error {
	// Direct, not topic: one kind of message to one queue, so a pattern match would
	// only be a slower way to say the same thing.
	for _, exchange := range []string{ExchangeRetry, ExchangeParked} {
		if err := channel.ExchangeDeclare(exchange, "direct", true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %q: %w", exchange, err)
		}
	}

	for _, subscription := range subscriptions {
		if err := declareSubscription(channel, subscription, config); err != nil {
			return fmt.Errorf("declare subscription %q: %w", subscription.Name, err)
		}
	}

	return nil
}

func declareSubscription(
	channel *amqp.Channel,
	subscription Subscription,
	config TopologyConfig,
) error {
	source := subscription.Source
	if err := channel.ExchangeDeclare(
		source.Name,
		source.Kind,
		source.Durable,
		source.AutoDelete,
		source.Internal,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("declare exchange %q: %w", source.Name, err)
	}

	// Rejected without requeue, which dead-letters to the retry exchange instead of
	// putting the message back at the head of this queue.
	working := deadLetterTo(ExchangeRetry, subscription.internalRoutingKey())
	if err := declareQueue(channel, subscription.Queue(), working); err != nil {
		return err
	}

	if err := channel.QueueBind(
		subscription.Queue(),
		subscription.RoutingKey,
		source.Name,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind queue %q: %w", subscription.Queue(), err)
	}

	// No consumer here on purpose: the TTL expiring is what sends the message back.
	// This queue is itself a dead-letter source, so it needs the same at-least-once
	// settings as the working queue — getting that wrong on the return leg would lose
	// exactly the messages that had already failed once. It routes through the default
	// exchange rather than the producer's, which would re-fan it out to every consumer.
	retry := deadLetterTo("", subscription.Queue())
	retry[amqp.QueueMessageTTLArg] = config.RetryDelay.Milliseconds()

	if err := declareQueue(channel, subscription.RetryQueue(), retry); err != nil {
		return err
	}

	if err := channel.QueueBind(
		subscription.RetryQueue(),
		subscription.internalRoutingKey(),
		ExchangeRetry,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind queue %q: %w", subscription.RetryQueue(), err)
	}

	// A destination only: it dead-letters nowhere, so it needs no settings of its own.
	if err := declareQueue(channel, subscription.ParkedQueue(), quorumArguments()); err != nil {
		return err
	}

	if err := channel.QueueBind(
		subscription.ParkedQueue(),
		subscription.internalRoutingKey(),
		ExchangeParked,
		false,
		nil,
	); err != nil {
		return fmt.Errorf("bind queue %q: %w", subscription.ParkedQueue(), err)
	}

	return nil
}

func declareQueue(channel *amqp.Channel, name string, arguments amqp.Table) error {
	_, err := channel.QueueDeclare(name, true, false, false, false, arguments)
	if err != nil {
		return fmt.Errorf("declare queue %q: %w", name, err)
	}

	return nil
}

// Quorum because these queues hold an order's confirmation and must survive a broker
// restart. reject-publish comes with it: at-least-once dead-lettering requires it, and
// a full queue then refuses new messages instead of silently dropping old ones.
func quorumArguments() amqp.Table {
	return amqp.Table{
		amqp.QueueTypeArg:     amqp.QueueTypeQuorum,
		amqp.QueueOverflowArg: amqp.QueueOverflowRejectPublish,
	}
}

// RabbitMQ's default dead-lettering is at-most-once, so a message being moved can be
// lost. at-least-once is opt-in and needs a quorum queue with reject-publish overflow,
// which is why those three always appear together.
func deadLetterTo(exchange string, routingKey string) amqp.Table {
	arguments := quorumArguments()
	arguments["x-dead-letter-exchange"] = exchange
	arguments["x-dead-letter-strategy"] = "at-least-once"
	arguments["x-dead-letter-routing-key"] = routingKey

	return arguments
}
