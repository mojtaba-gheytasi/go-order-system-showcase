package inventorygrpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/correlation"
)

// Correlation returns a unary client interceptor that sends this request's id on
// to inventory.
//
// It is what makes inventory's opaque INTERNAL diagnosable. That failure tells
// this service only that the other one is broken; the id is the thread from a
// customer's failed order to the inventory log line explaining it.
func Correlation() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		request any,
		response any,
		connection *grpc.ClientConn,
		invoke grpc.UnaryInvoker,
		options ...grpc.CallOption,
	) error {
		requestID := correlation.FromContext(ctx)
		if requestID == "" {
			return invoke(ctx, method, request, response, connection, options...)
		}

		ctx = metadata.AppendToOutgoingContext(ctx, correlation.MetadataKey, requestID)

		return invoke(ctx, method, request, response, connection, options...)
	}
}
