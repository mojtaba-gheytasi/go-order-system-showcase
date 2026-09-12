package httpgin

import (
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type moneyResponse struct {
	AmountInCents int64  `json:"amount_in_cents"`
	Currency      string `json:"currency"`
}

type orderItemResponse struct {
	LineNumber int           `json:"line_number"`
	ProductSKU string        `json:"product_sku"`
	Quantity   int           `json:"quantity"`
	UnitPrice  moneyResponse `json:"unit_price"`
}

type orderResponse struct {
	ID            string              `json:"id"`
	CustomerID    string              `json:"customer_id"`
	CustomerEmail string              `json:"customer_email"`
	Status        string              `json:"status"`
	Items         []orderItemResponse `json:"items"`
	Total         moneyResponse       `json:"total"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

func orderResponseFrom(order *domain.Order) orderResponse {
	orderItems := order.OrderItems()
	items := make([]orderItemResponse, 0, len(orderItems))
	for index, orderItem := range orderItems {
		items = append(items, orderItemResponse{
			// Line numbers are 1-based and come from position, matching how the
			// items are stored.
			LineNumber: index + 1,
			ProductSKU: orderItem.ProductSKU(),
			Quantity:   orderItem.Quantity(),
			UnitPrice: moneyResponse{
				AmountInCents: orderItem.UnitPrice().AmountInCents,
				Currency:      orderItem.UnitPrice().Currency,
			},
		})
	}

	total := order.Total()

	return orderResponse{
		ID:            string(order.ID()),
		CustomerID:    string(order.CustomerID()),
		CustomerEmail: order.CustomerEmail(),
		Status:        string(order.Status()),
		Items:         items,
		Total: moneyResponse{
			AmountInCents: total.AmountInCents,
			Currency:      total.Currency,
		},
		CreatedAt: order.CreatedAt(),
		UpdatedAt: order.UpdatedAt(),
	}
}
