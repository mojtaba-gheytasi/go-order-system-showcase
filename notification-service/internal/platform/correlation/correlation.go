// Package correlation carries a caller's request id through a call.
//
// An INTERNAL tells a caller only that this server is broken, deliberately not
// how. The request id is what closes that gap: it is echoed in the error and
// written on every log line, so the caller's failure and this server's
// explanation of it can be found together afterwards.
package correlation

import "context"

// gRPC lowercases metadata keys, so it is written that way to match what arrives.
const MetadataKey = "x-request-id"

const MaxLength = 64

type contextKey struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, contextKey{}, requestID)
}

func FromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(contextKey{}).(string)

	return requestID
}

// Acceptable restricts the character set rather than trusting it: ids reach logs, and
// anything unexpected is dropped rather than refusing otherwise valid work.
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
