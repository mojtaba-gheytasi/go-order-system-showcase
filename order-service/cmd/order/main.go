package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/bootstrap"
)

const (
	configPath  = "."
	serviceName = "order-service"
)

func main() {
	err := bootstrap.Run(context.Background(), bootstrap.Options{
		ConfigPath:  configPath,
		ServiceName: serviceName,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}
