package grpcapi_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

// These go through the server rather than calling the converters directly: what
// matters is the shape that reaches the wire, not the function that built it.

func TestInsufficientStockCarriesEveryShortfall(t *testing.T) {
	server := newServer(&application.InsufficientStockError{
		Shortfalls: []application.Shortfall{
			{ProductSKU: "SKU-A", Requested: 5, Available: 2},
			{ProductSKU: "SKU-B", Requested: 3, Available: 0},
		},
	})

	_, err := server.ReserveStock(context.Background(), testRequest())

	detail := detailOf[*inventoryv1.InsufficientStockDetail](t, err)
	require.Len(t, detail.GetShortfalls(), 2)
	assert.Equal(t, "SKU-A", detail.GetShortfalls()[0].GetProductSku())
	assert.Equal(t, int32(5), detail.GetShortfalls()[0].GetRequested())
	assert.Equal(t, int32(2), detail.GetShortfalls()[0].GetAvailable())
	assert.Equal(t, "SKU-B", detail.GetShortfalls()[1].GetProductSku())
}

func TestUnknownProductsCarriesEverySKU(t *testing.T) {
	server := newServer(&application.UnknownProductSKUError{ProductSKUs: []string{"SKU-X", "SKU-Y"}})

	_, err := server.ReserveStock(context.Background(), testRequest())

	detail := detailOf[*inventoryv1.UnknownProductsDetail](t, err)
	assert.Equal(t, []string{"SKU-X", "SKU-Y"}, detail.GetProductSkus())
}

func TestIdempotencyConflictCarriesBothSides(t *testing.T) {
	server := newServer(&application.IdempotencyConflictError{
		Held:      []application.Line{{ProductSKU: "SKU-A", Quantity: 9}},
		Requested: []application.Line{{ProductSKU: "SKU-B", Quantity: 1}},
	})

	_, err := server.ReserveStock(context.Background(), testRequest())

	detail := detailOf[*inventoryv1.IdempotencyConflictDetail](t, err)
	require.Len(t, detail.GetHeld(), 1)
	require.Len(t, detail.GetRequested(), 1)
	assert.Equal(t, "SKU-A", detail.GetHeld()[0].GetProductSku())
	assert.Equal(t, int32(9), detail.GetHeld()[0].GetQuantity())
	assert.Equal(t, "SKU-B", detail.GetRequested()[0].GetProductSku())
}

func TestInvalidRequestReportsFieldViolations(t *testing.T) {
	server := newServer(&application.InvalidRequestError{
		Violations: []application.FieldViolation{
			{Field: "lines[1].quantity", Description: "must be between 1 and 10000, got 0"},
		},
		Omitted: 3,
	})

	_, err := server.ReserveStock(context.Background(), testRequest())

	detail := detailOf[*errdetails.BadRequest](t, err)
	require.Len(t, detail.GetFieldViolations(), 2)
	assert.Equal(t, "lines[1].quantity", detail.GetFieldViolations()[0].GetField())
	assert.Contains(
		t,
		detail.GetFieldViolations()[1].GetDescription(),
		"3 further violations",
		"what was left out is still accounted for",
	)
}
