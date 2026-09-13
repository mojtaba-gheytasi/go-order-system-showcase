// Package wiring is the notification bounded context's composition root: the single
// place allowed to know every concrete adapter sitting behind the application
// layer's ports. Nothing under application/ or adapter/ may import it.
package wiring

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/inbound/amqpapi"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/outbound/emaillog"
	notificationpostgres "github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
)

type Dependencies struct {
	DB     *sql.DB
	Logger zerolog.Logger

	Consumer amqpapi.Config

	// SendLease bounds the effect rather than the delivery, so it belongs to the use
	// case and not the consumer.
	SendLease time.Duration
}

// Module is a struct rather than the bare consumer so a second inbound transport can
// arrive as another field without this signature changing.
type Module struct {
	Consumer *amqpapi.Consumer
}

// Three pieces where order-service has four, and no domain package: deduplication is a
// contention decision settled by one SQL statement, which is the same test that leaves
// inventory-service without one.
func New(dependencies Dependencies) (*Module, error) {
	handler := application.NewHandleOrderAccepted(
		notificationpostgres.NewSentNotificationStore(dependencies.DB),
		emaillog.NewSender(dependencies.Logger),
		dependencies.SendLease,
		dependencies.Logger,
	)

	// Adding another event is one more entry here plus the file that builds it.
	subscriptions := []amqpapi.Subscription{
		amqpapi.OrderAcceptedSubscription(handler),
	}

	consumer, err := amqpapi.NewConsumer(
		dependencies.Consumer,
		subscriptions,
		dependencies.Logger,
	)
	if err != nil {
		return nil, fmt.Errorf("build the consumer: %w", err)
	}

	return &Module{Consumer: consumer}, nil
}
