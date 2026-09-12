package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/correlation"
)

// This file is the whole failure taxonomy of the service, in one place.
//
// It is keyed on the application's errors rather than on any one RPC, so a
// second method that fails the same ways is answered by the same mapping
// without adding to it. Only a genuinely new failure mode adds a case here.

// The domain and reasons published in the ErrorInfo on every failure. The
// reason is the stable machine-readable name of what went wrong; the status
// code stays coarse because that is what generic middleware can act on.
const (
	errorDomain = "inventory.v1"

	reasonInvalidRequest      = "INVALID_REQUEST"
	reasonInsufficientStock   = "INSUFFICIENT_STOCK"
	reasonUnknownProductSKU   = "UNKNOWN_PRODUCT_SKU"
	reasonIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	reasonInternal            = "INTERNAL"
)

// statusFor maps an application error onto the status code that tells the caller
// what to do about it. The distinction that matters most is whether retrying
// could ever produce a different answer: only the codes gRPC itself raises for
// an unreachable or slow server mean "try again".
//
// Where the application error carries more than the code can express, it is
// attached as a status detail. Details ride on the failure, so a caller that
// reads only the code is unaffected by any of this.
func (server *Server) statusFor(ctx context.Context, err error) error {
	var (
		invalidRequest      *application.InvalidRequestError
		insufficientStock   *application.InsufficientStockError
		unknownProductSKU   *application.UnknownProductSKUError
		idempotencyConflict *application.IdempotencyConflictError
	)

	switch {
	case errors.As(err, &invalidRequest):
		return server.status(
			ctx,
			codes.InvalidArgument,
			"the request could not be validated",
			reasonInvalidRequest,
			badRequestFrom(invalidRequest),
		)

	case errors.As(err, &insufficientStock):
		return server.status(
			ctx,
			codes.FailedPrecondition,
			"insufficient stock",
			reasonInsufficientStock,
			insufficientStockDetailFrom(insufficientStock),
		)

	// The two sentinel cases below are reachable: the store answers with the
	// plain error when it cannot say more — a diagnosis query that failed, or a
	// shortfall that no longer exists because the warehouse was restocked. The
	// other errors have no such path and are only ever raised typed, so a bare
	// one would be a bug and is better surfaced as INTERNAL than quietly mapped.
	case errors.Is(err, application.ErrInsufficientStock):
		return server.status(ctx, codes.FailedPrecondition, "insufficient stock", reasonInsufficientStock)

	case errors.As(err, &unknownProductSKU):
		return server.status(
			ctx,
			codes.NotFound,
			"unknown product sku",
			reasonUnknownProductSKU,
			&inventoryv1.UnknownProductsDetail{ProductSkus: unknownProductSKU.ProductSKUs},
		)

	case errors.Is(err, application.ErrUnknownProductSKU):
		return server.status(ctx, codes.NotFound, "unknown product sku", reasonUnknownProductSKU)

	case errors.As(err, &idempotencyConflict):
		return server.status(
			ctx,
			codes.AlreadyExists,
			"this order id was already used for a different set of lines",
			reasonIdempotencyConflict,
			&inventoryv1.IdempotencyConflictDetail{
				Held:      reservationLinesFrom(idempotencyConflict.Held),
				Requested: reservationLinesFrom(idempotencyConflict.Requested),
			},
		)

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the call was cancelled")

	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the call ran out of time")

	default:
		// Nothing about how this server broke may reach a caller. The handler
		// logs what it knows about the request that hit it.
		return server.status(ctx, codes.Internal, "an unexpected error occurred", reasonInternal)
	}
}

// status assembles the outgoing error: the code, an ErrorInfo naming what went
// wrong in stable terms, and whatever details describe it further.
//
// A detail that will not marshal must not change the verdict. If WithDetails
// fails, the caller still gets the right code — a marshalling problem here
// would otherwise turn a correct FAILED_PRECONDITION into an INTERNAL.
func (server *Server) status(
	ctx context.Context,
	code codes.Code,
	message string,
	reason string,
	details ...protoadapt.MessageV1,
) error {
	plain := status.New(code, message)

	metadata := map[string]string{}
	if requestID := correlation.FromContext(ctx); requestID != "" {
		metadata["request_id"] = requestID
	}

	all := make([]protoadapt.MessageV1, 0, len(details)+1)
	all = append(all, &errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   errorDomain,
		Metadata: metadata,
	})
	all = append(all, details...)

	detailed, err := plain.WithDetails(all...)
	if err != nil {
		server.logger.Warn().
			Ctx(ctx).
			Err(err).
			Str("grpc_code", code.String()).
			Msg("could not attach status details")

		return plain.Err()
	}

	return detailed.Err()
}
