// Package bootstrap is the process-wide composition root. It owns the resources
// a running inventory-service binary needs — configuration, the database pool,
// the gRPC server — and hands them to the bounded-context wiring that knows what
// to build with them.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/wiring"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/config"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/database"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/grpcserver"
)

type App struct {
	logger  zerolog.Logger
	address string
	db      *sql.DB
	server  *grpcserver.Server
}

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

	inventoryModule, err := wiring.New(wiring.Dependencies{
		DB:     db,
		Logger: logger,
	})
	if err != nil {
		// No App is returned, so nobody will ever call Close on this pool.
		_ = db.Close()

		return nil, fmt.Errorf("wire the inventory context: %w", err)
	}

	return &App{
		logger:  logger,
		address: applicationConfig.GRPCServerAddress,
		db:      db,
		server: grpcserver.NewServer(
			grpcserver.Config{
				Address:              applicationConfig.GRPCServerAddress,
				MaxConnectionIdle:    applicationConfig.GRPCMaxConnectionIdle,
				MaxConnectionAge:     applicationConfig.GRPCMaxConnectionAge,
				MaxConnectionAgeSlop: applicationConfig.GRPCMaxConnectionAgeSlop,
				UnaryInterceptors:    inventoryModule.UnaryInterceptors,
			},
			inventoryModule.Register,
		),
	}, nil
}

func (app *App) Start() error {
	app.logger.Info().Str("address", app.address).Msg("gRPC server listening")

	return app.server.Start()
}

func (app *App) Shutdown(ctx context.Context) error {
	return app.server.Shutdown(ctx)
}

func (app *App) Close() error {
	return app.db.Close()
}
