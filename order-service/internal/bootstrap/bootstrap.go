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

	// The worker stops taking new events as soon as a signal arrives, while HTTP
	// is still draining. Events stored meanwhile wait in the outbox for the next
	// process, so nothing is lost by stopping it early.
	workerCtx, stopWorker := context.WithCancel(signalCtx)
	workerDone := make(chan struct{})
	go func() {
		app.RunEventWorker(workerCtx)
		close(workerDone)
	}()

	// A defer, so the worker is stopped before Close on every way out of Run,
	// including a server that failed to start.
	defer func() {
		stopWorker()
		if waitFor(workerDone, shutdownTimeout) == false {
			// Close would wait for the publisher the worker is stuck in. The
			// process is exiting anyway, and the event it holds is claimed again
			// by the next process once its lease runs out.
			logger.Warn().Msg("event worker did not stop in time; skipping cleanup")

			return
		}

		_ = app.Close()
		logger.Info().Msg("shutdown complete")
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

	return nil
}

// waitFor reports whether done closed within the timeout.
func waitFor(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
