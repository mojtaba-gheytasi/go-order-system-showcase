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
	Code    string `json:"code"`
	Message string `json:"message"`

	// Details names what has to change for the request to succeed, where that
	// can be said precisely. Omitted rather than empty when it cannot, so a
	// client can tell "nothing more to say" from "nothing was wrong".
	Details   any    `json:"details,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

// shortfallBody reports a line the warehouse could not cover. Available is
// published deliberately: it is what lets a customer adjust the quantity in one
// step instead of guessing down from the amount they asked for.
type shortfallBody struct {
	ProductSKU string `json:"product_sku"`
	Requested  int32  `json:"requested"`
	Available  int32  `json:"available"`
}

func respondError(context *gin.Context, status int, code string, message string) {
	respondErrorWithDetails(context, status, code, message, nil)
}

func respondErrorWithDetails(
	context *gin.Context,
	status int,
	code string,
	message string,
	details any,
) {
	context.AbortWithStatusJSON(status, errorResponse{
		Error: errorBody{
			Code:      code,
			Message:   message,
			Details:   details,
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

// respondUseCaseError shapes an application error into a response.
//
// The errors.As cases come first, and the errors.Is cases behind them are not
// redundant: they answer the same failures when nothing more is known. An
// insufficient-stock verdict replayed from this service's own database has no
// shortfall attached to it, and still has to be answered.
func respondUseCaseError(context *gin.Context, err error) {
	_ = context.Error(err)

	var (
		insufficientStock *application.InsufficientStockError
		productNotFound   *application.ProductNotFoundError
	)

	switch {
	case errors.As(err, &productNotFound):
		respondErrorWithDetails(
			context,
			http.StatusUnprocessableEntity,
			codeProductNotFound,
			"one or more products do not exist",
			gin.H{"product_skus": productNotFound.ProductSKUs},
		)
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
	case errors.As(err, &insufficientStock):
		respondErrorWithDetails(
			context,
			http.StatusConflict,
			codeInsufficientStock,
			"one or more items are not available in the requested quantity",
			gin.H{"shortfalls": shortfallBodiesFrom(insufficientStock.Shortfalls)},
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

func shortfallBodiesFrom(shortfalls []application.Shortfall) []shortfallBody {
	bodies := make([]shortfallBody, 0, len(shortfalls))
	for _, shortfall := range shortfalls {
		bodies = append(bodies, shortfallBody{
			ProductSKU: shortfall.ProductSKU,
			Requested:  shortfall.Requested,
			Available:  shortfall.Available,
		})
	}

	return bodies
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
