package postgres

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type orderRow struct {
	id                 uuid.UUID
	customerID         uuid.UUID
	customerEmail      string
	status             string
	reservationID      uuid.NullUUID
	idempotencyKey     string
	totalAmountInCents int64
	currency           string
	createdAt          time.Time
	updatedAt          time.Time
}

type orderItemRow struct {
	orderID           uuid.UUID
	lineNumber        int
	productSKU        string
	quantity          int
	unitAmountInCents int64
}

func rowsFromDomain(order *domain.Order) (orderRow, []orderItemRow, error) {
	orderID, err := uuid.Parse(string(order.ID()))
	if err != nil {
		return orderRow{}, nil, fmt.Errorf("parse order id: %w", err)
	}

	customerID, err := uuid.Parse(string(order.CustomerID()))
	if err != nil {
		return orderRow{}, nil, fmt.Errorf("parse customer id: %w", err)
	}

	reservationID, err := nullableUUID(order.ReservationID())
	if err != nil {
		return orderRow{}, nil, err
	}

	total := order.Total()
	storedOrder := orderRow{
		id:                 orderID,
		customerID:         customerID,
		customerEmail:      order.CustomerEmail(),
		status:             string(order.Status()),
		reservationID:      reservationID,
		idempotencyKey:     order.IdempotencyKey(),
		totalAmountInCents: total.AmountInCents,
		currency:           total.Currency,
		createdAt:          order.CreatedAt(),
		updatedAt:          order.UpdatedAt(),
	}

	orderItems := order.OrderItems()
	storedOrderItems := make([]orderItemRow, 0, len(orderItems))
	// Line numbers are 1-based and come from position: an order item has no
	// identity beyond where it sits on the order.
	for index, orderItem := range orderItems {
		storedOrderItems = append(storedOrderItems, orderItemRow{
			orderID:           orderID,
			lineNumber:        index + 1,
			productSKU:        orderItem.ProductSKU(),
			quantity:          orderItem.Quantity(),
			unitAmountInCents: orderItem.UnitPrice().AmountInCents,
		})
	}

	return storedOrder, storedOrderItems, nil
}

func domainFromRows(storedOrder orderRow, storedOrderItems []orderItemRow) (*domain.Order, error) {
	orderItems := make([]domain.OrderItem, 0, len(storedOrderItems))
	for _, storedOrderItem := range storedOrderItems {
		orderItem, err := domain.NewOrderItem(
			storedOrderItem.productSKU,
			storedOrderItem.quantity,
			domain.Money{
				AmountInCents: storedOrderItem.unitAmountInCents,
				Currency:      storedOrder.currency,
			},
		)
		if err != nil {
			return nil, fmt.Errorf(
				"map stored order item at line %d: %w",
				storedOrderItem.lineNumber,
				err,
			)
		}

		orderItems = append(orderItems, orderItem)
	}

	var reservationID domain.ReservationID
	if storedOrder.reservationID.Valid {
		reservationID = domain.ReservationID(storedOrder.reservationID.UUID.String())
	}

	order, err := domain.Reconstitute(
		domain.OrderID(storedOrder.id.String()),
		domain.CustomerID(storedOrder.customerID.String()),
		storedOrder.customerEmail,
		domain.Status(storedOrder.status),
		reservationID,
		storedOrder.idempotencyKey,
		orderItems,
		domain.Money{
			AmountInCents: storedOrder.totalAmountInCents,
			Currency:      storedOrder.currency,
		},
		storedOrder.createdAt,
		storedOrder.updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("corrupt persisted order %s: %w", storedOrder.id, err)
	}

	return order, nil
}

func nullableUUID(id domain.ReservationID) (uuid.NullUUID, error) {
	if id == "" {
		return uuid.NullUUID{}, nil
	}

	parsed, err := uuid.Parse(string(id))
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("parse reservation id: %w", err)
	}

	return uuid.NullUUID{UUID: parsed, Valid: true}, nil
}
