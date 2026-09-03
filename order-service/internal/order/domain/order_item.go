package domain

import (
	"fmt"
	"strings"
)

type Money struct {
	AmountInCents int64
	Currency      string
}

// OrderItem is a value object inside the Order aggregate. It deliberately has no
// identity: nothing addresses an order item on its own, and its position on the
// order is the only thing that distinguishes it.
type OrderItem struct {
	productSKU string
	quantity   int
	unitPrice  Money
}

func NewOrderItem(productSKU string, quantity int, unitPrice Money) (OrderItem, error) {
	orderItem := OrderItem{
		productSKU: strings.TrimSpace(productSKU),
		quantity:   quantity,
		unitPrice:  unitPrice,
	}

	if err := orderItem.validate(); err != nil {
		return OrderItem{}, err
	}

	return orderItem, nil
}

func (orderItem OrderItem) ProductSKU() string {
	return orderItem.productSKU
}

func (orderItem OrderItem) Quantity() int {
	return orderItem.quantity
}

func (orderItem OrderItem) UnitPrice() Money {
	return orderItem.unitPrice
}

func (orderItem OrderItem) validate() error {
	if strings.TrimSpace(orderItem.productSKU) == "" {
		return fmt.Errorf("%w: product SKU is required", ErrInvalidOrderItem)
	}

	if orderItem.quantity <= 0 {
		return fmt.Errorf("%w: quantity must be positive", ErrInvalidOrderItem)
	}

	if orderItem.unitPrice.AmountInCents < 0 {
		return fmt.Errorf("%w: amount in cents cannot be negative", ErrInvalidMoney)
	}

	if isCurrencySupported(orderItem.unitPrice.Currency) == false {
		return fmt.Errorf("%w: currency must be EUR or USD", ErrInvalidMoney)
	}

	return nil
}

func isCurrencySupported(currency string) bool {
	return currency == "EUR" || currency == "USD"
}
