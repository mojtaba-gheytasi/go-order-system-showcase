package application_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

const testOrderID = "018f0f38-5a52-7a01-8000-000000000010"

// The two properties the rest of the service depends on: the same request
// written differently must reduce to identical lines, and the result must be
// sorted, because that ordering is what stops two reservations deadlocking on
// each other's stock rows.
func TestCanonicalizeSumsDuplicatesAndSorts(t *testing.T) {
	_, lines, err := application.Canonicalize(testOrderID, []application.Line{
		{ProductSKU: "SKU-B", Quantity: 1},
		{ProductSKU: "SKU-A", Quantity: 2},
		{ProductSKU: "SKU-B", Quantity: 4},
	})
	require.NoError(t, err)

	assert.Equal(t, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 2},
		{ProductSKU: "SKU-B", Quantity: 5},
	}, lines)
}

func TestCanonicalizeIsOrderIndependent(t *testing.T) {
	first := []application.Line{
		{ProductSKU: "SKU-B", Quantity: 1},
		{ProductSKU: "SKU-A", Quantity: 2},
	}
	second := []application.Line{
		{ProductSKU: "SKU-A", Quantity: 2},
		{ProductSKU: "SKU-B", Quantity: 1},
	}

	_, canonicalFirst, err := application.Canonicalize(testOrderID, first)
	require.NoError(t, err)
	_, canonicalSecond, err := application.Canonicalize(testOrderID, second)
	require.NoError(t, err)

	// Idempotency compares stored lines against incoming ones. If permuting the
	// request changed the result, a retry would look like a different request.
	assert.Equal(t, canonicalFirst, canonicalSecond)
}

func TestCanonicalizeRejectsBadRequests(t *testing.T) {
	tests := map[string]struct {
		orderID string
		lines   []application.Line
	}{
		"order id is not a uuid": {
			orderID: "not-a-uuid",
			lines:   []application.Line{{ProductSKU: "SKU-A", Quantity: 1}},
		},
		"no lines": {
			orderID: testOrderID,
			lines:   nil,
		},
		"blank product sku": {
			orderID: testOrderID,
			lines:   []application.Line{{ProductSKU: "   ", Quantity: 1}},
		},
		"zero quantity": {
			orderID: testOrderID,
			lines:   []application.Line{{ProductSKU: "SKU-A", Quantity: 0}},
		},
		"negative quantity": {
			orderID: testOrderID,
			lines:   []application.Line{{ProductSKU: "SKU-A", Quantity: -1}},
		},
		"single quantity beyond the cap": {
			orderID: testOrderID,
			lines:   []application.Line{{ProductSKU: "SKU-A", Quantity: 100_001}},
		},
		"summed quantity beyond the cap": {
			orderID: testOrderID,
			lines: []application.Line{
				{ProductSKU: "SKU-A", Quantity: 60_000},
				{ProductSKU: "SKU-A", Quantity: 60_000},
			},
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := application.Canonicalize(testCase.orderID, testCase.lines)

			require.ErrorIs(t, err, application.ErrInvalidRequest)
		})
	}
}
