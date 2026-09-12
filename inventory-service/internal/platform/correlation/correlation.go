// Package correlation carries a caller's request id through a call.
//
// An INTERNAL tells a caller only that this server is broken, deliberately not
// how. The request id is what closes that gap: it is echoed in the error and
// written on every log line, so the caller's failure and this server's
// explanation of it can be found together afterwards.
package correlation

import "context"

// MetadataKey is the gRPC metadata key carrying the id. gRPC lowercases
// metadata keys, so it is written that way here to match what arrives.
const MetadataKey = "x-request-id"

// MaxLength bounds what is accepted from a caller. An id is echoed into logs
// and error details, so its size cannot be the caller's decision.
const MaxLength = 64

type contextKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, contextKey{}, requestID)
}

// FromContext returns the request id, or an empty string when the call arrived
// without one. A missing id is normal — it means the caller does not use them —
// so it is never an error.
func FromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(contextKey{}).(string)

	return requestID
}

// Acceptable filters what a caller sends. Request ids reach logs and error
// details, so the character set is restricted rather than trusted: anything
// unexpected is dropped and the call proceeds without an id.
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
