package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

func Logging(base zerolog.Logger, quietPaths ...string) gin.HandlerFunc {
	quiet := make(map[string]struct{}, len(quietPaths))
	for _, path := range quietPaths {
		quiet[path] = struct{}{}
	}

	return func(context *gin.Context) {
		start := time.Now()

		requestLogger := base.With().
			Str("request_id", RequestIDFromContext(context.Request.Context())).
			Str("http_method", context.Request.Method).
			Str("http_route", context.FullPath()).
			Logger()

		context.Request = context.Request.WithContext(
			requestLogger.WithContext(context.Request.Context()),
		)

		context.Next()

		if _, skip := quiet[context.Request.URL.Path]; skip {
			return
		}

		status := context.Writer.Status()

		event := requestLogger.Info()
		switch {
		case status >= 500:
			event = requestLogger.Error()
		case status >= 400:
			event = requestLogger.Warn()
		}

		if len(context.Errors) > 0 {
			event = event.Str("errors", context.Errors.String())
		}

		event.
			Int("http_status", status).
			Int64("duration_ms", time.Since(start).Milliseconds()).
			Int("response_bytes", context.Writer.Size()).
			Str("client_ip", context.ClientIP()).
			Msg("http request")
	}
}
