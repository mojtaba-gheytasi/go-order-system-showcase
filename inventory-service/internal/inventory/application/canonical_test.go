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

// A caller that fixes one field at a time needs one round trip per mistake, so
// everything wrong with a request is answered at once.
func TestCanonicalizeReportsEveryViolationAtOnce(t *testing.T) {
	_, _, err := application.Canonicalize("not-a-uuid", []application.Line{
		{ProductSKU: "SKU-A", Quantity: 1},
		{ProductSKU: "  ", Quantity: 1},
		{ProductSKU: "SKU-C", Quantity: 0},
	})

	var invalid *application.InvalidRequestError
	require.ErrorAs(t, err, &invalid)
	require.ErrorIs(t, err, application.ErrInvalidRequest, "the sentinel still answers")

	// Field paths index the request as the caller sent it, so they are
	// zero-based and name the line that has to change.
	assert.Equal(t, []application.FieldViolation{
		{Field: "order_id", Description: "must be a uuid"},
		{Field: "lines[1].product_sku", Description: "must not be blank"},
		{Field: "lines[2].quantity", Description: "must be between 1 and 10000, got 0"},
	}, invalid.Violations)
	assert.Zero(t, invalid.Omitted)
}

// A request can be wrong in as many places as it has lines. How large the error
// response gets cannot be the caller's decision.
func TestCanonicalizeCapsTheViolationsItReports(t *testing.T) {
	lines := make([]application.Line, 0, 30)
	for range 30 {
		lines = append(lines, application.Line{ProductSKU: "SKU-A", Quantity: 0})
	}

	_, _, err := application.Canonicalize(testOrderID, lines)

	var invalid *application.InvalidRequestError
	require.ErrorAs(t, err, &invalid)
	assert.Len(t, invalid.Violations, 20)
	assert.Equal(t, 10, invalid.Omitted, "the rest are counted, not listed")
}

// Past the line cap the request is refused whole, so walking its lines could
// only describe a request that is already rejected.
func TestCanonicalizeDoesNotDescribeLinesItNeverValidates(t *testing.T) {
	lines := make([]application.Line, 0, 201)
	for range 201 {
		lines = append(lines, application.Line{ProductSKU: "", Quantity: 0})
	}

	_, _, err := application.Canonicalize(testOrderID, lines)

	var invalid *application.InvalidRequestError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []application.FieldViolation{
		{Field: "lines", Description: "at most 200 lines are supported, got 201"},
	}, invalid.Violations)
	assert.Zero(t, invalid.Omitted)
}
