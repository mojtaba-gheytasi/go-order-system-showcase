// Package bootstrap is the process-wide composition root. It owns the
// resources a running order-service binary needs — configuration, the database
// pool, the HTTP server — and hands them to the bounded-context wiring that
// knows what to build with them.
package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/orderevents"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/inventorygrpc"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/wiring"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/config"
	platformdatabase "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/database"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/httpserver"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/rabbitmq"
)

type App struct {
	logger         zerolog.Logger
	address        string
	db             *sql.DB
	inventoryConn  *grpc.ClientConn
	eventPublisher *rabbitmq.Publisher
	eventWorker    *application.PublishOrderEvents
	server         *httpserver.Server
}

func New(
	ctx context.Context,
	applicationConfig config.Config,
	logger zerolog.Logger,
) (*App, error) {
	db, err := platformdatabase.New(ctx, platformdatabase.Config{
		URL:             applicationConfig.DatabaseURL,
		MaxOpenConns:    applicationConfig.DBMaxOpenConns,
		MaxIdleConns:    applicationConfig.DBMaxIdleConns,
		ConnMaxLifetime: applicationConfig.DBConnMaxLifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	logger.Info().Msg("connected to PostgreSQL")

	// grpc.NewClient does not connect here: it resolves the target and returns
	// a lazily-connecting client. That is deliberate — inventory being down must
	// not stop this service from booting and serving the endpoints that do not
	// need it. A call made while inventory is unreachable fails with
	// codes.Unavailable, which is exactly the retryable case.
	inventoryConn, err := grpc.NewClient(
		applicationConfig.InventoryGRPCAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(inventorygrpc.Correlation()),
	)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("create inventory client: %w", err)
	}

	// Like the gRPC client above, this does not connect here. It dials on its
	// first publication and redials after a failure, so a broker that is down
	// delays nothing at startup. Events wait in the outbox until it is back.
	//
	// The exchange is declared from the contract's own constants rather than from
	// configuration. Its name and settings are part of what order-service
	// publishes, not something an operator should be able to vary per environment.
	eventPublisher := rabbitmq.NewPublisher(
		rabbitmq.Config{
			URL:            applicationConfig.RabbitMQURL,
			PublishTimeout: applicationConfig.RabbitMQPublishTimeout,
			DialTimeout:    applicationConfig.RabbitMQPublishTimeout,
			ReconnectDelay: applicationConfig.RabbitMQReconnectDelay,
			Exchanges: []rabbitmq.ExchangeDeclaration{{
				Name:       orderevents.ExchangeOrders,
				Kind:       orderevents.ExchangeOrdersKind,
				Durable:    orderevents.ExchangeOrdersDurable,
				AutoDelete: orderevents.ExchangeOrdersAutoDelete,
				Internal:   orderevents.ExchangeOrdersInternal,
			}},
		},
		logger,
	)

	orderModule, err := wiring.New(wiring.Dependencies{
		DB:                 db,
		Logger:             logger,
		Clock:              func() time.Time { return time.Now().UTC() },
		NewID:              newIDGenerator(logger),
		InventoryConn:      inventoryConn,
		InventoryTimeout:   applicationConfig.InventoryGRPCTimeout,
		EventPublisher:     eventPublisher,
		OutboxPollInterval: applicationConfig.OutboxPollInterval,
		OutboxLease:        applicationConfig.OutboxClaimLease,
	})
	if err != nil {
		// No App is returned, so nobody will close these.
		_ = eventPublisher.Close()
		_ = inventoryConn.Close()
		_ = db.Close()

		return nil, fmt.Errorf("wire the order context: %w", err)
	}

	return &App{
		logger:         logger,
		address:        applicationConfig.HTTPServerAddress,
		db:             db,
		inventoryConn:  inventoryConn,
		eventPublisher: eventPublisher,
		eventWorker:    orderModule.EventWorker,
		server: httpserver.NewServer(
			httpserver.Config{
				Address:           applicationConfig.HTTPServerAddress,
				ReadHeaderTimeout: applicationConfig.HTTPReadHeaderTimeout,
				ReadTimeout:       applicationConfig.HTTPReadTimeout,
				WriteTimeout:      applicationConfig.HTTPWriteTimeout,
				IdleTimeout:       applicationConfig.HTTPIdleTimeout,
			},
			orderModule.HTTPHandler,
		),
	}, nil
}

func newIDGenerator(logger zerolog.Logger) func() string {
	return func() string {
		id, err := uuid.NewV7()
		if err != nil {
			logger.Warn().Err(err).Msg("generate uuid v7, falling back to v4")

			return uuid.NewString()
		}

		return id.String()
	}
}

// Start blocks until the server stops. A graceful shutdown returns nil.
func (app *App) Start() error {
	app.logger.Info().Str("address", app.address).Msg("HTTP server listening")

	return app.server.Start()
}

// RunEventWorker publishes stored events until ctx is cancelled. Close must not
// be called until it has returned: it uses the database and the publisher.
func (app *App) RunEventWorker(ctx context.Context) {
	app.logger.Info().Msg("event worker started")
	app.eventWorker.Run(ctx)
	app.logger.Info().Msg("event worker stopped")
}

// Shutdown drains in-flight requests, bounded by ctx.
func (app *App) Shutdown(ctx context.Context) error {
	return app.server.Shutdown(ctx)
}

// Close releases the resources New acquired. Call it after Shutdown and after
// RunEventWorker has returned, so nothing is still using them.
//
// errors.Join rather than an early return: a failure closing any one of these
// must not leave the others open.
func (app *App) Close() error {
	return errors.Join(
		app.eventPublisher.Close(),
		app.inventoryConn.Close(),
		app.db.Close(),
	)
}
