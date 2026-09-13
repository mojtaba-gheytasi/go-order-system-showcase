// Package bootstrap is the process-wide composition root. It owns the resources a
// running notification-service binary needs — configuration, the database pool, the
// broker consumer — and hands them to the bounded-context wiring that knows what to
// build with them.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/inbound/amqpapi"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/wiring"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/platform/config"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/platform/database"
)

// retryDelay is how long a failed delivery waits before being tried again.
const retryDelay = 30 * time.Second

type App struct {
	logger   zerolog.Logger
	db       *sql.DB
	consumer *amqpapi.Consumer
}

// The broker connection is deliberately absent from this list. The consumer dials in
// its own loop, so a broker that is down delays startup by nothing and is retried
// without special handling — the same reasoning that leaves order-service's gRPC
// client and publisher lazily connected.
func New(
	ctx context.Context,
	applicationConfig config.Config,
	logger zerolog.Logger,
) (*App, error) {
	db, err := database.New(ctx, database.Config{
		URL:             applicationConfig.DatabaseURL,
		MaxOpenConns:    applicationConfig.DBMaxOpenConns,
		MaxIdleConns:    applicationConfig.DBMaxIdleConns,
		ConnMaxLifetime: applicationConfig.DBConnMaxLifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	logger.Info().Msg("connected to PostgreSQL")

	notificationModule, err := wiring.New(wiring.Dependencies{
		DB:     db,
		Logger: logger,
		Consumer: amqpapi.Config{
			URL:            applicationConfig.RabbitMQURL,
			PrefetchCount:  applicationConfig.RabbitMQPrefetchCount,
			ReconnectDelay: applicationConfig.RabbitMQReconnectDelay,
			PublishTimeout: applicationConfig.RabbitMQPublishTimeout,
			HandleTimeout:  applicationConfig.HandleTimeout,
			RetryDelay:     retryDelay,
		},
		SendLease: applicationConfig.SendLease,
	})
	if err != nil {
		// No App is returned, so nobody will ever close this pool.
		_ = db.Close()

		return nil, fmt.Errorf("wire the notification context: %w", err)
	}

	return &App{
		logger:   logger,
		db:       db,
		consumer: notificationModule.Consumer,
	}, nil
}

func (app *App) Start(ctx context.Context) error {
	return app.consumer.Run(ctx)
}

func (app *App) Drain() {
	app.consumer.Wait()
}

func (app *App) Close() error {
	return app.db.Close()
}
