package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/correlation"
)

// HeaderRequestID is re-exported so handlers and tests in this package keep
// naming the header through the middleware that sets it.
const HeaderRequestID = correlation.HeaderRequestID

const generatedRequestIDBytes = 16

// RequestID gives every request a correlation id
func RequestID() gin.HandlerFunc {
	return func(context *gin.Context) {
		requestID := correlation.Acceptable(context.GetHeader(HeaderRequestID))
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
	return correlation.WithRequestID(ctx, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	return correlation.FromContext(ctx)
}

func generateRequestID() string {
	buffer := make([]byte, generatedRequestIDBytes)
	// crypto/rand.Read never returns an error as of Go 1.24.
	_, _ = rand.Read(buffer)

	return hex.EncodeToString(buffer)
}
