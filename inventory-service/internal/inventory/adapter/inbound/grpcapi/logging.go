package grpcapi

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/correlation"
)

// Logging returns a unary interceptor that writes one structured line per call.
//
// The method name is a fixed set decided by the service definition, so it is
// safe to log directly — unlike a URL path, it cannot be varied by a caller into
// unbounded label cardinality.
func Logging(logger zerolog.Logger, skipMethods ...string) grpc.UnaryServerInterceptor {
	skipped := make(map[string]struct{}, len(skipMethods))
	for _, method := range skipMethods {
		skipped[method] = struct{}{}
	}

	return func(
		ctx context.Context,
		request any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if _, skip := skipped[info.FullMethod]; skip {
			return handler(ctx, request)
		}

		startedAt := time.Now()
		response, err := handler(ctx, request)
		code := status.Code(err)

		event := logger.Info()
		// A non-OK code is the outcome of the call, not necessarily a fault:
		// "insufficient stock" is a correct answer. Only the codes that mean
		// this server misbehaved are logged as errors.
		if code == codes.Internal || code == codes.Unknown || code == codes.DataLoss {
			event = logger.Error()
		}

		event = event.
			Str("grpc_method", info.FullMethod).
			Str("grpc_code", code.String()).
			Dur("duration_ms", time.Since(startedAt))

		if requestID := correlation.FromContext(ctx); requestID != "" {
			event = event.Str("request_id", requestID)
		}

		event.Msg("grpc call")

		return response, err
	}
}
