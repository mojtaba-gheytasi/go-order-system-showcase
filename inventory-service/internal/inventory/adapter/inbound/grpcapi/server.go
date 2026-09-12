// Package grpcapi is the inbound adapter: it translates between the generated
// protobuf types and the application layer, and turns application errors into
// gRPC status codes. No proto type crosses into the application package.
//
// The package is split by job rather than by RPC, because the three jobs grow
// along different axes: this file holds the handlers, errors.go holds the
// failure taxonomy shared by all of them, and convert.go holds the translation
// between application and protobuf types.
package grpcapi

import (
	"context"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/correlation"
)

type ReserveStockUseCase interface {
	Execute(ctx context.Context, command application.ReserveStockCommand) error
}

type Server struct {
	inventoryv1.UnimplementedInventoryServiceServer

	reserveStock ReserveStockUseCase
	logger       zerolog.Logger
}

func NewServer(reserveStock ReserveStockUseCase, logger zerolog.Logger) *Server {
	return &Server{reserveStock: reserveStock, logger: logger}
}

func (server *Server) ReserveStock(
	ctx context.Context,
	request *inventoryv1.ReserveStockRequest,
) (*inventoryv1.ReserveStockResponse, error) {
	lines := make([]application.Line, 0, len(request.GetLines()))
	for _, line := range request.GetLines() {
		lines = append(lines, application.Line{
			ProductSKU: line.GetProductSku(),
			Quantity:   line.GetQuantity(),
		})
	}

	err := server.reserveStock.Execute(ctx, application.ReserveStockCommand{
		OrderID: request.GetOrderId(),
		Lines:   lines,
	})
	if err != nil {
		failure := server.statusFor(ctx, err)

		// Whether a failure is this server's fault is the taxonomy's verdict;
		// what identifies the request that hit it is the handler's own
		// knowledge. The detail stays here: a caller learns that this server is
		// broken, not how, and the request id is the thread back to this line.
		if status.Code(failure) == codes.Internal {
			server.logger.Error().
				Ctx(ctx).
				Err(err).
				Str("order_id", request.GetOrderId()).
				Str("request_id", correlation.FromContext(ctx)).
				Msg("reserve stock failed unexpectedly")
		}

		return nil, failure
	}

	// Nothing to report but success: the caller's order id already names the
	// stock now held for it.
	return &inventoryv1.ReserveStockResponse{}, nil
}
