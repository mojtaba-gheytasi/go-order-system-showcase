package domain

import (
	"fmt"
	"math"
	"net/mail"
	"slices"
	"strings"
	"time"
)

type OrderID string
type CustomerID string

type Order struct {
	id             OrderID
	customerID     CustomerID
	customerEmail  string
	status         Status
	idempotencyKey string
	orderItems     []OrderItem
	total          Money
	createdAt      time.Time
	updatedAt      time.Time
}

func NewOrder(
	id OrderID,
	customerID CustomerID,
	email string,
	idempotencyKey string,
	orderItems []OrderItem,
	now time.Time,
) (*Order, error) {
	total, err := calculateTotal(orderItems)
	if err != nil {
		return nil, err
	}

	order := &Order{
		id:             OrderID(strings.TrimSpace(string(id))),
		customerID:     CustomerID(strings.TrimSpace(string(customerID))),
		customerEmail:  strings.TrimSpace(email),
		status:         StatusPending,
		idempotencyKey: idempotencyKey,
		orderItems:     slices.Clone(orderItems),
		total:          total,
		createdAt:      now,
		updatedAt:      now,
	}

	if err := order.validate(); err != nil {
		return nil, err
	}

	return order, nil
}

func Reconstitute(
	id OrderID,
	customerID CustomerID,
	email string,
	status Status,
	idempotencyKey string,
	orderItems []OrderItem,
	total Money,
	createdAt time.Time,
	updatedAt time.Time,
) (*Order, error) {
	order := &Order{
		id:             id,
		customerID:     customerID,
		customerEmail:  email,
		status:         status,
		idempotencyKey: idempotencyKey,
		orderItems:     slices.Clone(orderItems),
		total:          total,
		createdAt:      createdAt,
		updatedAt:      updatedAt,
	}

	if err := order.validate(); err != nil {
		return nil, fmt.Errorf("reconstitute order: %w", err)
	}

	return order, nil
}

// Accept marks the order as backed by reserved stock. It receives the `now` from
// the caller so domain tests are deterministic.
//
// There is no reservation identifier to record. Inventory holds stock against
// this order's own id, so `accepted` is itself the statement that the stock was
// secured — a second identifier would only repeat the order id back.
func (order *Order) Accept(now time.Time) error {
	return order.transitionTo(StatusAccepted, now)
}

func (order *Order) Reject(now time.Time) error {
	return order.transitionTo(StatusRejected, now)
}

func (order *Order) ID() OrderID {
	return order.id
}

func (order *Order) CustomerID() CustomerID {
	return order.customerID
}

func (order *Order) CustomerEmail() string {
	return order.customerEmail
}

func (order *Order) Status() Status {
	return order.status
}

func (order *Order) IdempotencyKey() string {
	return order.idempotencyKey
}

func (order *Order) OrderItems() []OrderItem {
	return slices.Clone(order.orderItems)
}

func (order *Order) Total() Money {
	return order.total
}

func (order *Order) CreatedAt() time.Time {
	return order.createdAt
}

func (order *Order) UpdatedAt() time.Time {
	return order.updatedAt
}

func (order *Order) transitionTo(status Status, now time.Time) error {
	if order.status.canTransitionTo(status) == false {
		return fmt.Errorf(
			"%w: %s to %s",
			ErrInvalidStatusTransition,
			order.status,
			status,
		)
	}

	if now.IsZero() || now.Before(order.updatedAt) {
		return fmt.Errorf("%w: transition time is invalid", ErrInvalidOrder)
	}

	order.status = status
	order.updatedAt = now

	return nil
}

func (order *Order) validate() error {
	if strings.TrimSpace(string(order.id)) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidOrder)
	}

	if strings.TrimSpace(string(order.customerID)) == "" {
		return fmt.Errorf("%w: customer id is required", ErrInvalidOrder)
	}

	if isEmailValid(order.customerEmail) == false {
		return fmt.Errorf("%w: customer email is invalid", ErrInvalidOrder)
	}

	if strings.TrimSpace(order.idempotencyKey) == "" {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidOrder)
	}

	if order.status.isValid() == false {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidOrder, order.status)
	}

	if order.createdAt.IsZero() || order.updatedAt.IsZero() || order.updatedAt.Before(order.createdAt) {
		return fmt.Errorf("%w: timestamps are invalid", ErrInvalidOrder)
	}

	calculatedTotal, err := calculateTotal(order.orderItems)
	if err != nil {
		return err
	}

	if calculatedTotal != order.total {
		return fmt.Errorf("%w: stored total does not match order items", ErrInvalidOrder)
	}

	return nil
}

func calculateTotal(orderItems []OrderItem) (Money, error) {
	if len(orderItems) == 0 {
		return Money{}, fmt.Errorf("%w: at least one item is required", ErrInvalidOrder)
	}

	currency := orderItems[0].unitPrice.Currency
	var amountInCents int64

	for _, orderItem := range orderItems {
		if err := orderItem.validate(); err != nil {
			return Money{}, err
		}

		if orderItem.unitPrice.Currency != currency {
			return Money{}, fmt.Errorf("%w: item currencies do not match", ErrInvalidMoney)
		}

		quantity := int64(orderItem.quantity)
		if orderItem.unitPrice.AmountInCents > math.MaxInt64/quantity {
			return Money{}, fmt.Errorf("%w: item total overflows int64", ErrInvalidMoney)
		}

		lineAmountInCents := orderItem.unitPrice.AmountInCents * quantity
		if amountInCents > math.MaxInt64-lineAmountInCents {
			return Money{}, fmt.Errorf("%w: order total overflows int64", ErrInvalidMoney)
		}

		amountInCents += lineAmountInCents
	}

	return Money{AmountInCents: amountInCents, Currency: currency}, nil
}

func isEmailValid(email string) bool {
	if strings.TrimSpace(email) != email || email == "" {
		return false
	}

	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}
