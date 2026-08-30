package main

import (
	"fmt"
	"os"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/inbound/httpgin"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/config"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/httpserver"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/observability"
)

const (
	configPath  = "."
	serviceName = "order-service"
)

func main() {
	applicationConfig, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load order service configuration: %v\n", err)
		os.Exit(1)
	}

	logger, err := observability.NewLogger(observability.LoggerConfig{
		ServiceName:   serviceName,
		Environment:   applicationConfig.Environment,
		Level:         applicationConfig.LogLevel,
		IncludeCaller: *applicationConfig.LogCaller,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create order service logger: %v\n", err)
		os.Exit(1)
	}

	router := httpgin.NewRouter()

	server := httpserver.NewServer(httpserver.Config{
		Address:           applicationConfig.HTTPServerAddress,
		ReadHeaderTimeout: applicationConfig.HTTPReadHeaderTimeout,
		ReadTimeout:       applicationConfig.HTTPReadTimeout,
		WriteTimeout:      applicationConfig.HTTPWriteTimeout,
		IdleTimeout:       applicationConfig.HTTPIdleTimeout,
	}, router)

	logger.Info().
		Str("address", applicationConfig.HTTPServerAddress).
		Msg("starting HTTP server")

	if err := server.Start(); err != nil {
		logger.Error().
			Err(err).
			Msg("HTTP server stopped with an error")

		os.Exit(1)
	}
}
