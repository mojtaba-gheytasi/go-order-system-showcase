// Package wiring is the inventory bounded context's composition root: the single
// place allowed to know every concrete adapter sitting behind the application
// layer's ports. Nothing under application/ or adapter/ may import it.
package wiring

import (
	"database/sql"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/adapter/inbound/grpcapi"
	inventorypostgres "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

const healthMethod = "/grpc.health.v1.Health/Check"

type Dependencies struct {
	DB     *sql.DB
	Logger zerolog.Logger
}

// Module is what the inventory context contributes to the process. A struct
// rather than the two bare values so that a second inbound transport, or a
// background worker, can arrive as another field without changing this signature.
type Module struct {
	Register          func(*grpc.Server)
	UnaryInterceptors []grpc.UnaryServerInterceptor
}

func New(dependencies Dependencies) (*Module, error) {
	reserveStock := application.NewReserveStock(
		inventorypostgres.NewReservationStore(dependencies.DB),
		dependencies.Logger,
	)

	inventoryServer := grpcapi.NewServer(reserveStock, dependencies.Logger)

	return &Module{
		Register: func(server *grpc.Server) {
			inventoryv1.RegisterInventoryServiceServer(server, inventoryServer)

			// Liveness over gRPC's own health protocol rather than an HTTP
			// endpoint: this service speaks one transport, and adding a second
			// listener for a single probe would be the larger change.
			// SERVING is reported unconditionally — it says the process is up,
			// deliberately not that PostgreSQL is, so that a brief database
			// outage does not make an orchestrator restart working pods.
			healthServer := health.NewServer()
			healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
			healthv1.RegisterHealthServer(server, healthServer)
		},
		// Health checks run every few seconds forever; logging them would bury
		// the calls that carry information.
		UnaryInterceptors: []grpc.UnaryServerInterceptor{
			grpcapi.Logging(dependencies.Logger, healthMethod),
		},
	}, nil
}
