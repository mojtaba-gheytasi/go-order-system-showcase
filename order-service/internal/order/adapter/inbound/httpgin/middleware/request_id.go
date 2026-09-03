package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

const HeaderRequestID = "X-Request-ID"

const (
	maxInboundRequestIDLength = 64
	generatedRequestIDBytes   = 16
)

type requestIDContextKey struct{}

// RequestID gives every request a correlation id
func RequestID() gin.HandlerFunc {
	return func(context *gin.Context) {
		requestID := acceptableRequestID(context.GetHeader(HeaderRequestID))
		if requestID == "" {
			requestID = generateRequestID()
		}

		context.Writer.Header().Set(HeaderRequestID, requestID)
		context.Request = context.Request.WithContext(
			WithRequestID(context.Request.Context(), requestID),
		)

		context.Next()
	}
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)

	return requestID
}

func acceptableRequestID(requestID string) string {
	if requestID == "" || len(requestID) > maxInboundRequestIDLength {
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

func generateRequestID() string {
	buffer := make([]byte, generatedRequestIDBytes)
	// crypto/rand.Read never returns an error as of Go 1.24.
	_, _ = rand.Read(buffer)

	return hex.EncodeToString(buffer)
}
