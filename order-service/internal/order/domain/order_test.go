package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

func TestNewOrderCalculatesTotalAndAcceptRequiresAReservation(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	orderItems := []domain.OrderItem{
		newOrderItem(t, "SKU-B", 1, 500),
		newOrderItem(t, "SKU-A", 2, 1250),
	}

	order, err := domain.NewOrder(
		"018f0f38-5a52-7a01-8000-000000000010",
		"018f0f38-5a52-7a01-8000-000000000020",
		"customer@example.com",
		"create-order-1",
		orderItems,
		now,
	)
	require.NoError(t, err)

	assert.Equal(t, domain.StatusPending, order.Status())
	assert.Equal(t, domain.Money{AmountInCents: 3000, Currency: "EUR"}, order.Total())

	err = order.Accept("", now.Add(time.Minute))
	require.ErrorIs(t, err, domain.ErrReservationRequired)

	err = order.Accept(
		"018f0f38-5a52-7a01-8000-000000000030",
		now.Add(time.Minute),
	)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusAccepted, order.Status())
}

func TestPendingOrderCanBeRejected(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	order := newOrder(t, now)

	require.NoError(t, order.Reject(now.Add(time.Minute)))
	assert.Equal(t, domain.StatusRejected, order.Status())
	assert.Equal(t, now.Add(time.Minute), order.UpdatedAt())
}

func TestAcceptedAndRejectedOrdersCannotBeProcessedAgain(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)

	accepted := newOrder(t, now)
	require.NoError(t, accepted.Accept(
		"018f0f38-5a52-7a01-8000-000000000030",
		now.Add(time.Minute),
	))
	require.ErrorIs(t, accepted.Reject(now.Add(2*time.Minute)), domain.ErrInvalidStatusTransition)

	rejected := newOrder(t, now)
	require.NoError(t, rejected.Reject(now.Add(time.Minute)))
	require.ErrorIs(t, rejected.Accept(
		"018f0f38-5a52-7a01-8000-000000000030",
		now.Add(2*time.Minute),
	), domain.ErrInvalidStatusTransition)
}

// Order item position becomes the persisted line number, so the aggregate must
// preserve the order it was given rather than sorting it.
func TestNewOrderPreservesOrderItemPosition(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)

	order, err := domain.NewOrder(
		"018f0f38-5a52-7a01-8000-000000000010",
		"018f0f38-5a52-7a01-8000-000000000020",
		"customer@example.com",
		"create-order-1",
		[]domain.OrderItem{
			newOrderItem(t, "SKU-B", 1, 500),
			newOrderItem(t, "SKU-A", 2, 1250),
		},
		now,
	)
	require.NoError(t, err)

	orderItems := order.OrderItems()
	require.Len(t, orderItems, 2)
	assert.Equal(t, "SKU-B", orderItems[0].ProductSKU())
	assert.Equal(t, "SKU-A", orderItems[1].ProductSKU())
}

func newOrder(t *testing.T, now time.Time) *domain.Order {
	t.Helper()

	order, err := domain.NewOrder(
		"018f0f38-5a52-7a01-8000-000000000010",
		"018f0f38-5a52-7a01-8000-000000000020",
		"customer@example.com",
		"create-order-1",
		[]domain.OrderItem{newOrderItem(t, "SKU-A", 1, 1000)},
		now,
	)
	require.NoError(t, err)

	return order
}

func TestNewOrderItemRejectsUnsupportedCurrency(t *testing.T) {
	_, err := domain.NewOrderItem(
		"SKU-A",
		1,
		domain.Money{AmountInCents: 1000, Currency: "GBP"},
	)

	require.ErrorIs(t, err, domain.ErrInvalidMoney)
}

func newOrderItem(t *testing.T, sku string, quantity int, amountInCents int64) domain.OrderItem {
	t.Helper()

	orderItem, err := domain.NewOrderItem(
		sku,
		quantity,
		domain.Money{AmountInCents: amountInCents, Currency: "EUR"},
	)
	require.NoError(t, err)

	return orderItem
}
