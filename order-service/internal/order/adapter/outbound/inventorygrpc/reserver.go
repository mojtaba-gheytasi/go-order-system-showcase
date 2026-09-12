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
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
//
// Status details refine the answer where inventory sent them, but never decide
// it: the code alone determines what this returns, so a status carrying no
// details behaves exactly as it did before they existed.
func statusError(err error) error {
	statusValue := status.Convert(err)

	switch statusValue.Code() {
	case codes.FailedPrecondition:
		if shortfalls := shortfallsFrom(statusValue); len(shortfalls) > 0 {
			return &application.InsufficientStockError{Shortfalls: shortfalls}
		}

		return application.ErrInsufficientStock

	case codes.NotFound, codes.InvalidArgument, codes.AlreadyExists:
		// This service priced and validated the basket before calling, so a
		// refusal of the request itself is a defect rather than a customer
		// error. describeRejection gathers whatever inventory said about it,
		// because that text is what an operator has to work from.
		return fmt.Errorf("%w: %s", ErrInventoryRejectedRequest, describeRejection(statusValue))

	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: %s", application.ErrInventoryUnavailable, statusValue.Code())

	default:
		return fmt.Errorf("reserve stock: %w", err)
	}
}

func shortfallsFrom(statusValue *status.Status) []application.Shortfall {
	for _, detail := range statusValue.Details() {
		insufficientStock, matches := detail.(*inventoryv1.InsufficientStockDetail)
		if matches == false {
			continue
		}

		shortfalls := make([]application.Shortfall, 0, len(insufficientStock.GetShortfalls()))
		for _, shortfall := range insufficientStock.GetShortfalls() {
			shortfalls = append(shortfalls, application.Shortfall{
				ProductSKU: shortfall.GetProductSku(),
				Requested:  shortfall.GetRequested(),
				Available:  shortfall.GetAvailable(),
			})
		}

		return shortfalls
	}

	return nil
}

// describeRejection renders what inventory refused, for the log line an operator
// will read. An unknown sku here means this service's catalogue and the
// warehouse disagree about which products exist, and naming them is the
// difference between an alert somebody can act on and one they cannot.
func describeRejection(statusValue *status.Status) string {
	parts := []string{statusValue.Message()}

	for _, detail := range statusValue.Details() {
		switch typed := detail.(type) {
		case *inventoryv1.UnknownProductsDetail:
			parts = append(parts, "unknown skus: "+strings.Join(typed.GetProductSkus(), ", "))

		case *inventoryv1.IdempotencyConflictDetail:
			parts = append(parts, fmt.Sprintf(
				"order id holds %s but this request asked for %s",
				describeLines(typed.GetHeld()),
				describeLines(typed.GetRequested()),
			))

		case *errdetails.BadRequest:
			violations := make([]string, 0, len(typed.GetFieldViolations()))
			for _, violation := range typed.GetFieldViolations() {
				violations = append(
					violations,
					fmt.Sprintf("%s %s", violation.GetField(), violation.GetDescription()),
				)
			}

			parts = append(parts, strings.Join(violations, "; "))
		}
	}

	return strings.Join(parts, "; ")
}

func describeLines(lines []*inventoryv1.ReservationLine) string {
	descriptions := make([]string, 0, len(lines))
	for _, line := range lines {
		descriptions = append(
			descriptions,
			fmt.Sprintf("%s x%d", line.GetProductSku(), line.GetQuantity()),
		)
	}

	return "[" + strings.Join(descriptions, ", ") + "]"
}
