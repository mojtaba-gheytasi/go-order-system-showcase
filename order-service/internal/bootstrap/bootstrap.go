package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/config"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/observability"
)

const shutdownTimeout = 10 * time.Second

type Options struct {
	ConfigPath  string
	ServiceName string
}

func Run(ctx context.Context, options Options) error {
	applicationConfig, err := config.Load(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger, err := observability.NewLogger(observability.LoggerConfig{
		ServiceName:   options.ServiceName,
		Environment:   applicationConfig.Environment,
		Level:         applicationConfig.LogLevel,
		IncludeCaller: *applicationConfig.LogCaller,
	})
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}

	signalCtx, stopListeningForSignals := signal.NotifyContext(
		ctx,
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stopListeningForSignals()

	app, err := New(signalCtx, applicationConfig, logger)
	if err != nil {
		return err
	}
	defer func() {
		_ = app.Close()
	}()

	serverFailed := make(chan error, 1)
	go func() {
		serverFailed <- app.Start()
	}()

	select {
	case err := <-serverFailed:
		if err != nil {
			return fmt.Errorf("run HTTP server: %w", err)
		}

		return nil
	case <-signalCtx.Done():
		stopListeningForSignals()
	}

	logger.Info().Dur("timeout", shutdownTimeout).Msg("shutting down")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()

	if err := app.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down HTTP server: %w", err)
	}

	logger.Info().Msg("shutdown complete")

	return nil
}
