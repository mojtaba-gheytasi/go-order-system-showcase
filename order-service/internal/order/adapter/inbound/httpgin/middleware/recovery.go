package middleware

import (
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

// Recovery turns a panic into a response written by respond, and logs the stack
// through zerolog.
func Recovery(respond func(context *gin.Context)) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(context *gin.Context, recovered any) {
		zerolog.Ctx(context.Request.Context()).Error().
			Interface("panic", recovered).
			Str("stack", string(debug.Stack())).
			Msg("recovered from panic")

		respond(context)
	})
}
