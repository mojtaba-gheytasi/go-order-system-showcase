package httpgin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/inbound/httpgin/middleware"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

const (
	codeInvalidRequest       = "invalid_request"
	codeValidationFailed     = "validation_failed"
	codeProductNotFound      = "product_not_found"
	codeCatalogUnavailable   = "catalog_unavailable"
	codeInsufficientStock    = "insufficient_stock"
	codeInventoryUnavailable = "inventory_unavailable"
	codeInternal             = "internal_error"
)

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

func respondError(context *gin.Context, status int, code string, message string) {
	context.AbortWithStatusJSON(status, errorResponse{
		Error: errorBody{
			Code:      code,
			Message:   message,
			RequestID: middleware.RequestIDFromContext(context.Request.Context()),
		},
	})
}

func respondInternalError(context *gin.Context) {
	respondError(
		context,
		http.StatusInternalServerError,
		codeInternal,
		"an unexpected error occurred",
	)
}

func respondUseCaseError(context *gin.Context, err error) {
	_ = context.Error(err)

	switch {
	case errors.Is(err, application.ErrProductNotFound):
		respondError(
			context,
			http.StatusUnprocessableEntity,
			codeProductNotFound,
			"one or more products do not exist",
		)
	case errors.Is(err, application.ErrCatalogUnavailable):
		respondError(
			context,
			http.StatusServiceUnavailable,
			codeCatalogUnavailable,
			"the product catalog is temporarily unavailable, retry shortly",
		)
	case errors.Is(err, application.ErrInsufficientStock):
		respondError(
			context,
			http.StatusConflict,
			codeInsufficientStock,
			"one or more items are not available in the requested quantity",
		)
	case errors.Is(err, application.ErrInventoryUnavailable):
		respondError(
			context,
			http.StatusServiceUnavailable,
			codeInventoryUnavailable,
			"inventory is temporarily unavailable, retry shortly",
		)
	case errors.Is(err, domain.ErrInvalidOrder),
		errors.Is(err, domain.ErrInvalidOrderItem),
		errors.Is(err, domain.ErrInvalidMoney),
		errors.Is(err, domain.ErrInvalidStatusTransition):
		respondError(
			context,
			http.StatusUnprocessableEntity,
			codeValidationFailed,
			"the order was rejected by a business rule",
		)
	default:
		respondInternalError(context)
	}
}

func describeBindingError(err error) string {
	var validationErrors validator.ValidationErrors
	if errors.As(err, &validationErrors) == false {
		return "the request body could not be parsed"
	}

	failures := make([]string, 0, len(validationErrors))
	for _, fieldError := range validationErrors {
		failures = append(
			failures,
			fmt.Sprintf("%s failed the %q rule", fieldError.Field(), fieldError.Tag()),
		)
	}

	return strings.Join(failures, "; ")
}
