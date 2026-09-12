// Package inventorygrpc is the outbound adapter for inventory-service. It
// translates the application's ReservationRequest into the generated protobuf
// types, and inventory's gRPC status codes back into application errors.
//
// The mapping in statusError is the retry contract. CreateOrder retries only
// ErrInventoryUnavailable, so what this file decides is retryable is what
// actually gets retried.
package inventorygrpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
)

// ErrInventoryRejectedRequest means inventory refused the request itself rather
// than being unable to serve it: an unknown product, a malformed call, or an
// order id reused with different lines. Retrying cannot change the answer.
var ErrInventoryRejectedRequest = errors.New("inventory rejected the request")

type Reserver struct {
	client  inventoryv1.InventoryServiceClient
	timeout time.Duration
}

var _ application.InventoryReserver = (*Reserver)(nil)

// NewReserver wraps a connection. The timeout bounds a single attempt, not the
// whole retry sequence: without it a hung inventory would hold the request until
// the HTTP write timeout fired and the caller's remaining attempts would never
// happen.
func NewReserver(connection *grpc.ClientConn, timeout time.Duration) *Reserver {
	return &Reserver{
		client:  inventoryv1.NewInventoryServiceClient(connection),
		timeout: timeout,
	}
}

func (reserver *Reserver) Reserve(
	ctx context.Context,
	request application.ReservationRequest,
) error {
	lines := make([]*inventoryv1.ReservationLine, 0, len(request.Lines))
	for _, line := range request.Lines {
		lines = append(lines, &inventoryv1.ReservationLine{
			ProductSku: line.ProductSKU,
			Quantity:   int32(line.Quantity),
		})
	}

	attemptCtx, cancelAttempt := context.WithTimeout(ctx, reserver.timeout)
	defer cancelAttempt()

	_, err := reserver.client.ReserveStock(attemptCtx, &inventoryv1.ReserveStockRequest{
		OrderId: string(request.OrderID),
		Lines:   lines,
	})
	if err != nil {
		return statusError(err)
	}

	return nil
}

// statusError turns a gRPC status into the application's vocabulary.
//
// The question each case answers is whether trying again could produce a
// different result. Only Unavailable and DeadlineExceeded can: they mean the
// call never reached a verdict. Internal deliberately does not — it means a bug
// on the server, so three more attempts would fail identically while adding load
// to a service that is already in trouble.
func statusError(err error) error {
	switch status.Code(err) {
	case codes.FailedPrecondition:
		return application.ErrInsufficientStock

	case codes.NotFound, codes.InvalidArgument, codes.AlreadyExists:
		return fmt.Errorf("%w: %s", ErrInventoryRejectedRequest, status.Convert(err).Message())

	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: %s", application.ErrInventoryUnavailable, status.Code(err))

	default:
		return fmt.Errorf("reserve stock: %w", err)
	}
}
