package grpcapi

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/correlation"
)

// Correlation returns a unary interceptor that lifts the caller's request id out
// of the call metadata and into the context.
//
// A call without one is normal rather than an error: it means the caller does
// not use request ids, and nothing about the call changes.
func Correlation() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		incoming, found := metadata.FromIncomingContext(ctx)
		if found == false {
			return handler(ctx, request)
		}

		values := incoming.Get(correlation.MetadataKey)
		if len(values) == 0 {
			return handler(ctx, request)
		}

		// Anything unexpected is dropped rather than rejected. A malformed
		// correlation id is not a reason to refuse work that is otherwise valid.
		requestID := correlation.Acceptable(values[0])
		if requestID == "" {
			return handler(ctx, request)
		}

		return handler(correlation.WithRequestID(ctx, requestID), request)
	}
}
