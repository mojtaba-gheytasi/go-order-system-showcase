// Package bootstrap is the process-wide composition root. It owns the
// resources a running order-service binary needs — configuration, the database
// pool, the HTTP server — and hands them to the bounded-context wiring that
// knows what to build with them.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/wiring"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/config"
	platformdatabase "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/database"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/httpserver"
)

type App struct {
	logger  zerolog.Logger
	address string
	db      *sql.DB
	server  *httpserver.Server
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

	orderModule, err := wiring.New(wiring.Dependencies{
		DB:     db,
		Logger: logger,
		Clock:  func() time.Time { return time.Now().UTC() },
		NewID:  newIDGenerator(logger),
	})
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("wire the order context: %w", err)
	}

	return &App{
		logger:  logger,
		address: applicationConfig.HTTPServerAddress,
		db:      db,
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

// Shutdown drains in-flight requests, bounded by ctx.
func (app *App) Shutdown(ctx context.Context) error {
	return app.server.Shutdown(ctx)
}

// Close releases the resources New acquired. Call it after Shutdown.
func (app *App) Close() error {
	return app.db.Close()
}
