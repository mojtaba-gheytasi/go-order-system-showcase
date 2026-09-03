package inventorystub_test

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/inventorystub"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

func TestReserverReturnsTheSameReservationForTheSameOrder(t *testing.T) {
	generated := 0
	reserver := inventorystub.NewReserver(func() string {
		generated++
		return "reservation-id"
	}, zerolog.New(io.Discard))
	request := application.ReservationRequest{
		OrderID: "order-id",
		Lines: []application.ReservationLine{
			{ProductSKU: "SKU-A", Quantity: 2},
		},
	}

	first, err := reserver.Reserve(context.Background(), request)
	require.NoError(t, err)
	second, err := reserver.Reserve(context.Background(), request)
	require.NoError(t, err)

	assert.Equal(t, domain.ReservationID("reservation-id"), first)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, generated)
}
