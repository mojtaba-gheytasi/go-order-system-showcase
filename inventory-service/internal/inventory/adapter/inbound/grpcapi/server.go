// Package grpcapi is the inbound adapter: it translates between the generated
// protobuf types and the application layer, and turns application errors into
// gRPC status codes. No proto type crosses into the application package.
package grpcapi

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
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
		return nil, server.statusFor(ctx, request.GetOrderId(), err)
	}

	// Nothing to report but success: the caller's order id already names the
	// stock now held for it.
	return &inventoryv1.ReserveStockResponse{}, nil
}

// statusFor maps an application error onto the status code that tells the caller
// what to do about it. The distinction that matters most is whether retrying
// could ever produce a different answer: only the codes gRPC itself raises for
// an unreachable or slow server mean "try again".
func (server *Server) statusFor(ctx context.Context, orderID string, err error) error {
	switch {
	case errors.Is(err, application.ErrInvalidRequest):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, application.ErrInsufficientStock):
		return status.Error(codes.FailedPrecondition, "insufficient stock")

	case errors.Is(err, application.ErrUnknownProductSKU):
		return status.Error(codes.NotFound, "unknown product sku")

	case errors.Is(err, application.ErrIdempotencyConflict):
		return status.Error(
			codes.AlreadyExists,
			"this order id was already used for a different set of lines",
		)

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the call was cancelled")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the call ran out of time")

	default:
		// The detail stays in the log. A caller learns that this server is
		// broken, not how.
		server.logger.Error().
			Ctx(ctx).
			Err(err).
			Str("order_id", orderID).
			Msg("reserve stock failed unexpectedly")

		return status.Error(codes.Internal, "an unexpected error occurred")
	}
}
