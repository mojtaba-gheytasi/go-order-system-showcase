// Package rabbitmq publishes a message and reports what actually happened to it.
//
// AMQP gives three genuinely different answers, and collapsing them into success and
// failure would let this service log "no event was published" about an event a consumer
// is already processing:
//
//	routed           confirmed, and no return arrived
//	not routed       returned unroutable, nacked, or never sent
//	unknown          the write or the confirmation was cut short
//
// AN ACK IS NOT SUCCESS. A mandatory publication matching no queue is returned and may
// then be acknowledged too — the broker took responsibility, it just had nowhere to put
// the message. The return is delivered before that confirmation, which is what makes it
// safe to check for one afterwards.
package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
)

// The first three all mean the message was certainly not routed and differ only in why.
// ErrOutcomeUnknown must never be treated as either: retrying risks a duplicate, and
// reporting failure risks claiming nothing was sent when something was.
var (
	ErrUnavailable    = errors.New("broker unavailable")
	ErrUnroutable     = errors.New("no queue was bound to receive the message")
	ErrRejected       = errors.New("broker refused the message")
	ErrOutcomeUnknown = errors.New("publication outcome unknown")
)

// Declared so this service can publish before any consumer exists. Arguments come from
// the contract, because RabbitMQ fails the channel when two declarations disagree.
type ExchangeDeclaration struct {
	Name       string
	Kind       string
	Durable    bool
	AutoDelete bool
	Internal   bool
}

type Config struct {
	URL            string
	PublishTimeout time.Duration

	// ReconnectDelay keeps a down broker from being dialled once per inbound request.
	ReconnectDelay time.Duration

	Exchanges []ExchangeDeclaration
}

type Message struct {
	Exchange      string
	RoutingKey    string
	Type          string
	ContentType   string
	MessageID     string
	CorrelationID string
	Body          []byte
}

// Does not connect when constructed: a broker that is down must not stop this service
// starting, the same reasoning that leaves the inventory gRPC client lazily connected.
type Publisher struct {
	config Config
	logger zerolog.Logger

	// One publication at a time, so a return that arrives belongs to the message being
	// waited on and needs no correlation by delivery tag.
	mutex      sync.Mutex
	connection *amqp.Connection
	channel    *amqp.Channel
	returns    chan amqp.Return
	lastDialAt time.Time
}

func NewPublisher(config Config, logger zerolog.Logger) *Publisher {
	return &Publisher{config: config, logger: logger}
}

// A nil error means routed. Every other outcome is one of the sentinels above.
func (publisher *Publisher) Publish(ctx context.Context, message Message) error {
	publisher.mutex.Lock()
	defer publisher.mutex.Unlock()

	channel, returns, err := publisher.ready()
	if err != nil {
		return err
	}

	publishCtx, cancelPublish := context.WithTimeout(ctx, publisher.config.PublishTimeout)
	defer cancelPublish()

	// A leftover return would otherwise be read as this publication's verdict.
	drain(returns)

	confirmation, err := channel.PublishWithDeferredConfirmWithContext(
		publishCtx,
		message.Exchange,
		message.RoutingKey,
		// mandatory, which is the only reason an unroutable publication is
		// detectable rather than silently discarded.
		true,
		false,
		amqp.Publishing{
			DeliveryMode:  amqp.Persistent,
			ContentType:   message.ContentType,
			Type:          message.Type,
			MessageId:     message.MessageID,
			CorrelationId: message.CorrelationID,
			Timestamp:     time.Now().UTC(),
			Body:          message.Body,
		},
	)
	if err != nil {
		// Whether any bytes reached the broker is not knowable from here.
		publisher.discard()

		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}

	acknowledged, err := confirmation.WaitContext(publishCtx)
	if err != nil {
		// The broker may well have routed it already.
		publisher.discard()

		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}

	// Safe only now: a return is delivered before the confirmation.
	select {
	case returned := <-returns:
		return fmt.Errorf("%w: %s (%d)", ErrUnroutable, returned.ReplyText, returned.ReplyCode)
	default:
	}

	if acknowledged == false {
		return ErrRejected
	}

	return nil
}

// Safe to call when nothing was ever dialled.
func (publisher *Publisher) Close() error {
	publisher.mutex.Lock()
	defer publisher.mutex.Unlock()

	var channelErr, connectionErr error

	if publisher.channel != nil {
		channelErr = publisher.channel.Close()
		publisher.channel = nil
	}

	if publisher.connection != nil {
		connectionErr = publisher.connection.Close()
		publisher.connection = nil
	}

	publisher.returns = nil

	// Joined so a failing channel close does not leave the connection open.
	return errors.Join(channelErr, connectionErr)
}

// All four are rebuilt together on purpose. Reconnecting the socket alone would
// leave a channel with no return listener and no confirm mode — publishing would
// appear to work while quietly losing the ability to notice that nothing was
// listening. Recovery is therefore the same code path as the first connection,
// so there is only one version of it to be correct.
//
// Callers hold the mutex.
func (publisher *Publisher) ready() (*amqp.Channel, chan amqp.Return, error) {
	if publisher.channel != nil && publisher.channel.IsClosed() == false {
		return publisher.channel, publisher.returns, nil
	}

	publisher.discard()

	if waited := time.Since(publisher.lastDialAt); waited < publisher.config.ReconnectDelay {
		return nil, nil, fmt.Errorf(
			"%w: last dial %s ago, waiting %s between attempts",
			ErrUnavailable,
			waited.Round(time.Millisecond),
			publisher.config.ReconnectDelay,
		)
	}

	publisher.lastDialAt = time.Now()

	connection, err := amqp.Dial(publisher.config.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: dial: %w", ErrUnavailable, err)
	}

	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()

		return nil, nil, fmt.Errorf("%w: open channel: %w", ErrUnavailable, err)
	}

	for _, exchange := range publisher.config.Exchanges {
		err := channel.ExchangeDeclare(
			exchange.Name,
			exchange.Kind,
			exchange.Durable,
			exchange.AutoDelete,
			exchange.Internal,
			false,
			nil,
		)
		if err != nil {
			_ = channel.Close()
			_ = connection.Close()

			return nil, nil, fmt.Errorf(
				"%w: declare exchange %q: %w",
				ErrUnavailable,
				exchange.Name,
				err,
			)
		}
	}

	if err := channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = connection.Close()

		return nil, nil, fmt.Errorf("%w: enter confirm mode: %w", ErrUnavailable, err)
	}

	publisher.connection = connection
	publisher.channel = channel
	publisher.returns = channel.NotifyReturn(make(chan amqp.Return, 1))

	publisher.logger.Info().Msg("connected to RabbitMQ")

	return publisher.channel, publisher.returns, nil
}

// Callers hold the mutex.
func (publisher *Publisher) discard() {
	if publisher.channel != nil {
		_ = publisher.channel.Close()
		publisher.channel = nil
	}

	if publisher.connection != nil {
		_ = publisher.connection.Close()
		publisher.connection = nil
	}

	publisher.returns = nil
}

func drain(returns chan amqp.Return) {
	for {
		select {
		case <-returns:
		default:
			return
		}
	}
}
