package httpgin

import (
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/inbound/httpgin/middleware"
)

const (
	healthPath = "/healthz"
	ordersPath = "/orders"
)

type RouterConfig struct {
	Logger      zerolog.Logger
	CreateOrder CreateOrderUseCase
}

func NewRouter(config RouterConfig) http.Handler {
	useJSONFieldNamesInValidationErrors()

	router := gin.New()

	router.Use(
		middleware.RequestID(),
		middleware.Logging(config.Logger, healthPath),
		middleware.Recovery(respondInternalError),
	)

	// Gin trusts every proxy by default, which would let any caller forge the
	// client IP that ends up in each log line.
	_ = router.SetTrustedProxies(nil)

	router.HandleMethodNotAllowed = true
	router.NoRoute(func(context *gin.Context) {
		respondError(context, http.StatusNotFound, codeInvalidRequest, "no such endpoint")
	})
	router.NoMethod(func(context *gin.Context) {
		respondError(
			context,
			http.StatusMethodNotAllowed,
			codeInvalidRequest,
			"method not allowed for this endpoint",
		)
	})

	router.GET(healthPath, func(context *gin.Context) {
		context.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	orderHandler := NewOrderHandler(config.CreateOrder)
	router.POST(ordersPath, orderHandler.Create)

	return router
}

var registerJSONFieldNames sync.Once

func useJSONFieldNamesInValidationErrors() {
	registerJSONFieldNames.Do(func() {
		engine, ok := binding.Validator.Engine().(*validator.Validate)
		if ok == false {
			return
		}

		engine.RegisterTagNameFunc(func(field reflect.StructField) string {
			name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
			if name == "-" {
				return ""
			}

			return name
		})
	})
}
