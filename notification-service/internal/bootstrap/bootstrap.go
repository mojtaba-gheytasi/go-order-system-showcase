package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/platform/config"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/platform/observability"
)

type Options struct {
	ConfigPath  string
	ServiceName string
}

// Run starts the service and blocks until it stops, either because the consumer
// failed or because ctx was cancelled or the process was signalled. Every resource
// it acquires is released before it returns, so the caller only has to turn the error
// into an exit code.
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

	consumerFailed := make(chan error, 1)
	go func() {
		consumerFailed <- app.Start(signalCtx)
	}()

	select {
	case err := <-consumerFailed:
		if err != nil {
			return fmt.Errorf("run consumer: %w", err)
		}

		return nil
	case <-signalCtx.Done():
		// Restore default signal handling so a second interrupt kills the process
		// instead of being swallowed by a slow drain.
		stopListeningForSignals()
	}

	logger.Info().
		Dur("timeout", applicationConfig.ShutdownTimeout).
		Msg("shutting down")

	// Cancelling the context stops new deliveries being taken; this waits for the
	// ones already in hand. A handler killed between sending an email and recording
	// it would leave a claim held and cause a second email later, so finishing is
	// worth waiting for — but not forever.
	if drained := waitFor(app.Drain, applicationConfig.ShutdownTimeout); drained == false {
		logger.Warn().Msg("gave up waiting for in-flight deliveries")
	}

	logger.Info().Msg("shutdown complete")

	return nil
}

// waitFor runs drain and reports whether it finished inside the timeout.
//
// The goroutine is deliberately leaked on timeout: it is blocked on work this process
// is about to stop waiting for, and the process is exiting regardless.
func waitFor(drain func(), timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		drain()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
