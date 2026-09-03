package httpgin

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
)

const headerIdempotencyKey = "Idempotency-Key"

// CreateOrderUseCase is declared here, in the package that consumes it, so
// handler tests can substitute a fake without reaching into the application
// layer.
type CreateOrderUseCase interface {
	Execute(
		ctx context.Context,
		command application.CreateOrderCommand,
	) (application.CreateOrderResult, error)
}

type OrderHandler struct {
	createOrder CreateOrderUseCase
}

func NewOrderHandler(createOrder CreateOrderUseCase) *OrderHandler {
	return &OrderHandler{createOrder: createOrder}
}

// Create handles POST /orders. It does four things: read the
// request, turn it into a command, run the use case, and shape the result.
func (handler *OrderHandler) Create(context *gin.Context) {
	idempotencyKey := strings.TrimSpace(context.GetHeader(headerIdempotencyKey))
	if idempotencyKey == "" {
		respondError(
			context,
			http.StatusBadRequest,
			codeInvalidRequest,
			"the Idempotency-Key header is required",
		)

		return
	}

	var request createOrderRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		respondError(context, http.StatusBadRequest, codeInvalidRequest, describeBindingError(err))

		return
	}

	result, err := handler.createOrder.Execute(
		context.Request.Context(),
		request.toCommand(idempotencyKey),
	)
	if err != nil {
		respondUseCaseError(context, err)

		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}

	context.JSON(status, orderResponseFrom(result.Order))
}
