package httpgin

import (
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
)

type createOrderRequest struct {
	CustomerID    string                   `json:"customer_id"    binding:"required,uuid"`
	CustomerEmail string                   `json:"customer_email" binding:"required,email"`
	Items         []createOrderItemRequest `json:"items"          binding:"required,min=1,dive"`
}

type createOrderItemRequest struct {
	ProductSKU string `json:"product_sku" binding:"required"`
	Quantity   int    `json:"quantity"    binding:"required,gt=0"`
}

func (request createOrderRequest) toCommand(idempotencyKey string) application.CreateOrderCommand {
	items := make([]application.CreateOrderItem, 0, len(request.Items))
	for _, item := range request.Items {
		items = append(items, application.CreateOrderItem{
			ProductSKU: item.ProductSKU,
			Quantity:   item.Quantity,
		})
	}

	return application.CreateOrderCommand{
		IdempotencyKey: idempotencyKey,
		CustomerID:     request.CustomerID,
		CustomerEmail:  request.CustomerEmail,
		Items:          items,
	}
}
