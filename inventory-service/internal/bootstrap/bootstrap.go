package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/config"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/observability"
)

type Options struct {
	ConfigPath  string
	ServiceName string
}

// Run starts the service and blocks until it stops, either because the server
// failed or because ctx was cancelled or the process was signalled. Every
// resource it acquires is released before it returns, so the caller only has to
// turn the error into an exit code.
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

	// Signals are trapped before any slow dependency is built, so an impatient
	// Ctrl-C during a database connect is honoured.
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
			return fmt.Errorf("run gRPC server: %w", err)
		}

		return nil
	case <-signalCtx.Done():
		// Restore default signal handling so a second interrupt kills the
		// process instead of being swallowed by a slow drain.
		stopListeningForSignals()
	}

	logger.Info().
		Dur("timeout", applicationConfig.GRPCShutdownTimeout).
		Msg("shutting down")

	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.Background(),
		applicationConfig.GRPCShutdownTimeout,
	)
	defer cancelShutdown()

	if err := app.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down gRPC server: %w", err)
	}

	logger.Info().Msg("shutdown complete")

	return nil
}
