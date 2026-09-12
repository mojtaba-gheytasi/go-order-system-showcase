// Package correlation carries a request's id through the process.
//
// It lives in platform rather than in the HTTP middleware that populates it
// because both edges need it: the inbound adapter puts an id in the context,
// and the outbound gRPC adapter sends it on. An outbound adapter reaching into
// an inbound one for this would be the wrong direction entirely.
package correlation

import "context"

const (
	// HeaderRequestID is the HTTP header carrying the id, in and out.
	HeaderRequestID = "X-Request-ID"

	// MetadataKey is the same id on a gRPC call. gRPC lowercases metadata keys,
	// so it is written that way here to match what arrives at the other end.
	MetadataKey = "x-request-id"

	// MaxLength bounds what is accepted from a caller. An id is echoed into
	// logs and responses, so its size cannot be the caller's decision.
	MaxLength = 64
)

type contextKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, contextKey{}, requestID)
}

// FromContext returns the request id, or an empty string when there is none.
func FromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(contextKey{}).(string)

	return requestID
}

// Acceptable filters an id supplied by a caller. Request ids are written to logs
// and reflected in responses, so the character set is restricted rather than
// trusted: anything unexpected is dropped and a fresh id is generated instead.
func Acceptable(requestID string) string {
	if requestID == "" || len(requestID) > MaxLength {
		return ""
	}

	for _, character := range requestID {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-',
			character == '_':
		default:
			return ""
		}
	}

	return requestID
}
