// Package amqpapi is notification-service's inbound adapter: it takes deliveries off
// RabbitMQ, hands them to the use case that owns them, and translates the answer back
// into an AMQP action.
//
// The retry policy is three attempts, then park:
//
//	working queue --(rejected)--> retry queue --(TTL expires)--> working queue
//	working queue --(budget spent, or permanently invalid)--> parked queue
//
// The delay comes from the message expiring on the retry queue, not from this process
// sleeping. Rejecting with requeue=true would put the message straight back at the head
// of the working queue and hammer the failing provider with no pause.
package amqpapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/platform/correlation"
)

type Config struct {
	URL            string
	PrefetchCount  int
	ReconnectDelay time.Duration
	RetryDelay     time.Duration
	PublishTimeout time.Duration
	HandleTimeout  time.Duration
}

type Consumer struct {
	config        Config
	subscriptions []Subscription
	logger        zerolog.Logger

	// inFlight tracks handlers that have started, so shutdown can wait for them
	// rather than killing a delivery midway through a send.
	inFlight sync.WaitGroup
}

// Fails rather than starting with a broken topology: a missing routing key yields a
// queue nothing reaches, which looks exactly like "no messages" at runtime.
func NewConsumer(
	config Config,
	subscriptions []Subscription,
	logger zerolog.Logger,
) (*Consumer, error) {
	if len(subscriptions) == 0 {
		return nil, errors.New("a consumer needs at least one subscription")
	}

	queues := make(map[string]bool, len(subscriptions))
	for _, subscription := range subscriptions {
		if err := subscription.Validate(); err != nil {
			return nil, err
		}

		// Sharing a name would share all three queues.
		if queues[subscription.Name] {
			return nil, fmt.Errorf("duplicate subscription name %q", subscription.Name)
		}
		queues[subscription.Name] = true
	}

	return &Consumer{config: config, subscriptions: subscriptions, logger: logger}, nil
}

// Run consumes until ctx is cancelled.
//
// It reconnects on its own. Every iteration rebuilds the whole session — connection,
// channel, prefetch, topology, subscriptions — because a partially rebuilt session is
// the dangerous kind: a channel without its topology re-declared can consume from a
// queue that no longer has the binding it expects, and nothing about that looks like an
// error.
func (consumer *Consumer) Run(ctx context.Context) error {
	for {
		if err := consumer.consume(ctx); err != nil {
			if ctx.Err() != nil {
				// Cancelled while connected. Not a failure.
				return nil
			}

			consumer.logger.Error().
				Err(err).
				Dur("retry_in", consumer.config.ReconnectDelay).
				Msg("consumer session ended, reconnecting")
		}

		if ctx.Err() != nil {
			return nil
		}

		// A broker that is down must not be dialled in a tight loop.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(consumer.config.ReconnectDelay):
		}
	}
}

// Wait blocks until every started handler has finished. Called after Run returns, so a
// delivery that was mid-send is allowed to finish and record its result rather than
// being abandoned with its claim still held.
func (consumer *Consumer) Wait() {
	consumer.inFlight.Wait()
}

type inbound struct {
	subscription Subscription
	delivery     amqp.Delivery
}

func (consumer *Consumer) consume(ctx context.Context) error {
	connection, err := amqp.Dial(consumer.config.URL)
	if err != nil {
		return fmt.Errorf("dial broker: %w", err)
	}
	defer func() { _ = connection.Close() }()

	channel, err := connection.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() { _ = channel.Close() }()

	topology := TopologyConfig{RetryDelay: consumer.config.RetryDelay}
	if err := declareTopology(channel, consumer.subscriptions, topology); err != nil {
		return fmt.Errorf("declare topology: %w", err)
	}

	// Left at the default, the broker hands over entire queues at once.
	if err := channel.Qos(consumer.config.PrefetchCount, 0, false); err != nil {
		return fmt.Errorf("set prefetch: %w", err)
	}

	// Parking republishes, and an acknowledgement is not proof of routing.
	if err := channel.Confirm(false); err != nil {
		return fmt.Errorf("enter confirm mode: %w", err)
	}

	returns := channel.NotifyReturn(make(chan amqp.Return, 1))
	closed := channel.NotifyClose(make(chan *amqp.Error, 1))

	merged, err := consumer.merge(ctx, channel)
	if err != nil {
		return err
	}

	consumer.logger.Info().
		Int("subscriptions", len(consumer.subscriptions)).
		Int("prefetch", consumer.config.PrefetchCount).
		Msg("consuming")

	for {
		select {
		case <-ctx.Done():
			return nil
		case reason := <-closed:
			return fmt.Errorf("channel closed: %w", errorFrom(reason))
		case in, open := <-merged:
			if open == false {
				return errors.New("deliveries channel closed")
			}

			consumer.inFlight.Add(1)
			consumer.handle(ctx, channel, returns, in)
			consumer.inFlight.Done()
		}
	}
}

// One stream rather than a goroutine per subscription, because settlement must stay
// serial: with a single publication in flight, a return belongs to the message being
// waited on and needs no correlation by delivery tag.
func (consumer *Consumer) merge(
	ctx context.Context,
	channel *amqp.Channel,
) (<-chan inbound, error) {
	merged := make(chan inbound)

	var forwarders sync.WaitGroup

	for _, subscription := range consumer.subscriptions {
		// autoAck false: for most outcomes "done" is after a database write.
		deliveries, err := channel.Consume(
			subscription.Queue(),
			"",
			false,
			false,
			false,
			false,
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("consume %q: %w", subscription.Queue(), err)
		}

		forwarders.Add(1)
		go func(subscription Subscription, deliveries <-chan amqp.Delivery) {
			defer forwarders.Done()

			for delivery := range deliveries {
				select {
				case merged <- inbound{subscription: subscription, delivery: delivery}:
				case <-ctx.Done():
					return
				}
			}
		}(subscription, deliveries)
	}

	// Closing once every forwarder stops is what lets the settlement loop notice a
	// dead channel.
	go func() {
		forwarders.Wait()
		close(merged)
	}()

	return merged, nil
}

func (consumer *Consumer) handle(
	ctx context.Context,
	channel *amqp.Channel,
	returns chan amqp.Return,
	in inbound,
) {
	subscription, delivery := in.subscription, in.delivery

	// A context value does not reach a log line, so a child logger carries the id.
	requestID := correlation.Acceptable(delivery.CorrelationId)
	logger := consumer.logger.With().
		Str("subscription", subscription.Name).
		Str("message_id", delivery.MessageId).
		Int("transport_deaths", deathCount(delivery.Headers, subscription.Queue())).
		Logger()
	if requestID != "" {
		logger = logger.With().Str("request_id", requestID).Logger()
	}

	handleCtx := correlation.WithRequestID(ctx, requestID)

	// A queue bound with a wildcard receives keys nobody named individually, so what
	// the message claims to be matters more than how it arrived.
	if err := validateEnvelope(subscription, delivery); err != nil {
		logger.Error().Err(err).Msg("parking a message whose envelope is wrong")
		consumer.park(ctx, channel, returns, in, logger, "invalid_envelope")

		return
	}

	// A delivery this process will not start must go back untouched.
	if handleCtx.Err() != nil {
		consumer.settle(in, logger, application.Result{Outcome: application.OutcomeShuttingDown})

		return
	}

	// Bounded below the claim's lease: a handler outliving its claim would let another
	// worker reclaim the effect and send a second email.
	executeCtx, cancelExecute := context.WithTimeout(handleCtx, consumer.config.HandleTimeout)
	defer cancelExecute()

	result := subscription.Handle(executeCtx, delivery.MessageId, delivery.Body)

	logger = logger.With().
		Str("outcome", result.Outcome.String()).
		Int("attempts", result.Attempts).
		Logger()

	switch result.Outcome {
	case application.OutcomeInvalid:
		// The same bytes fail the same way forever.
		logger.Error().Err(result.Err).Msg("parking an event that cannot be handled")
		consumer.park(ctx, channel, returns, in, logger, "invalid_event")

	case application.OutcomeExhausted:
		logger.Error().Msg("send budget spent, parking for a human")
		consumer.park(ctx, channel, returns, in, logger, "attempts_exhausted")

	default:
		consumer.settle(in, logger, result)
	}
}

func validateEnvelope(subscription Subscription, delivery amqp.Delivery) error {
	if delivery.ContentType != subscription.ContentType {
		return fmt.Errorf(
			"content type %q, want %q",
			delivery.ContentType,
			subscription.ContentType,
		)
	}

	if delivery.Type != subscription.MessageType {
		return fmt.Errorf(
			"message type %q, want %q",
			delivery.Type,
			subscription.MessageType,
		)
	}

	return nil
}

// Exhaustive over a named type, so adding an outcome without deciding its AMQP action
// is a compile-time question rather than a message taking the default branch.
func (consumer *Consumer) settle(
	in inbound,
	logger zerolog.Logger,
	result application.Result,
) {
	switch result.Outcome {
	case application.OutcomeSent:
		logger.Info().Msg("notification handled")
		acknowledge(in.delivery, logger)

	case application.OutcomeAlreadySent:
		logger.Debug().Msg("already notified, nothing to do")
		acknowledge(in.delivery, logger)

	case application.OutcomeClaimBusy:
		// Back without spending an attempt: the budget counts provider calls.
		logger.Debug().Msg("another worker holds the claim, retrying later")
		reject(in.delivery, logger)

	case application.OutcomeTransientFailure:
		logger.Warn().Err(result.Err).Msg("sending failed, retrying later")
		reject(in.delivery, logger)

	case application.OutcomeStorageFailure:
		logger.Error().Err(result.Err).Msg("storage failed, retrying later")
		reject(in.delivery, logger)

	case application.OutcomeShuttingDown:
		logger.Debug().Msg("shutting down, returning the delivery")
		reject(in.delivery, logger)

	case application.OutcomeInvalid, application.OutcomeExhausted:
		// Parked before settle is reached, so arriving here is a bug. Rejecting is
		// the harmless direction.
		logger.Error().Msg("unexpected outcome at settlement, returning the delivery")
		reject(in.delivery, logger)

	default:
		logger.Error().Msg("unknown outcome, returning the delivery")
		reject(in.delivery, logger)
	}
}

// The original is acknowledged ONLY once the parked copy is confirmed and not returned:
// acknowledging first would destroy a message rather than park it, in the one place
// whose purpose is that nothing is lost silently. If parking fails the delivery is
// rejected instead, and the database's budget is what stops that sending again.
//
// Body and properties are preserved so a parked message can be republished; the reason
// goes in a header rather than the body.
func (consumer *Consumer) park(
	ctx context.Context,
	channel *amqp.Channel,
	returns chan amqp.Return,
	in inbound,
	logger zerolog.Logger,
	reason string,
) {
	subscription, delivery := in.subscription, in.delivery

	headers := amqp.Table{}
	for key, value := range delivery.Headers {
		headers[key] = value
	}
	headers["x-parked-reason"] = reason
	headers["x-parked-at"] = time.Now().UTC().Format(time.RFC3339)
	headers["x-parked-from-queue"] = subscription.Queue()

	publishCtx, cancelPublish := context.WithTimeout(
		context.WithoutCancel(ctx),
		consumer.config.PublishTimeout,
	)
	defer cancelPublish()

	drainReturns(returns)

	confirmation, err := channel.PublishWithDeferredConfirmWithContext(
		publishCtx,
		ExchangeParked,
		subscription.internalRoutingKey(),
		// mandatory, so a missing parked binding is an error rather than silence.
		true,
		false,
		amqp.Publishing{
			DeliveryMode:  amqp.Persistent,
			ContentType:   delivery.ContentType,
			Type:          delivery.Type,
			MessageId:     delivery.MessageId,
			CorrelationId: delivery.CorrelationId,
			Timestamp:     delivery.Timestamp,
			Headers:       headers,
			Body:          delivery.Body,
		},
	)
	if err != nil {
		logger.Error().Err(err).Msg("could not park the message, returning it instead")
		reject(delivery, logger)

		return
	}

	acknowledged, err := confirmation.WaitContext(publishCtx)
	if err != nil {
		logger.Error().Err(err).Msg("parking was not confirmed, returning the message instead")
		reject(delivery, logger)

		return
	}

	select {
	case returned := <-returns:
		logger.Error().
			Str("reply_text", returned.ReplyText).
			Msg("the parked queue is not bound, returning the message instead")
		reject(delivery, logger)

		return
	default:
	}

	if acknowledged == false {
		logger.Error().Msg("the broker refused the parked message, returning it instead")
		reject(delivery, logger)

		return
	}

	logger.Warn().Str("reason", reason).Msg("message parked")
	acknowledge(delivery, logger)
}

func acknowledge(delivery amqp.Delivery, logger zerolog.Logger) {
	if err := delivery.Ack(false); err != nil {
		// It will be redelivered; the claim in the database stops a resend.
		logger.Error().Err(err).Msg("could not acknowledge the delivery")
	}
}

// requeue=false is the mechanism: it dead-letters to the retry queue rather than
// putting the message back at the head of this one.
func reject(delivery amqp.Delivery, logger zerolog.Logger) {
	if err := delivery.Nack(false, false); err != nil {
		logger.Error().Err(err).Msg("could not reject the delivery")
	}
}

func drainReturns(returns chan amqp.Return) {
	for {
		select {
		case <-returns:
		default:
			return
		}
	}
}

func errorFrom(reason *amqp.Error) error {
	if reason == nil {
		return errors.New("closed without a reason")
	}

	return reason
}
