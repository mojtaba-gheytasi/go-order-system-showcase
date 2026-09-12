package grpcapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/correlation"
)

// The status code is the contract a caller's retry policy keys on, and the
// reason is the stable name of what went wrong. Both are asserted for every
// failure, because a detail must never be what decides the verdict.
//
// Only two sentinels appear without their typed error, because only those two
// are raised bare in production — the store answers that way when it cannot
// diagnose a refusal. The rest would be bugs, and map to INTERNAL by design.
func TestReserveStockMapsErrorsOntoCodesAndReasons(t *testing.T) {
	tests := map[string]struct {
		err    error
		code   codes.Code
		reason string
	}{
		"invalid request": {
			err:    &application.InvalidRequestError{},
			code:   codes.InvalidArgument,
			reason: "INVALID_REQUEST",
		},
		"insufficient stock": {
			err:    &application.InsufficientStockError{},
			code:   codes.FailedPrecondition,
			reason: "INSUFFICIENT_STOCK",
		},
		"insufficient stock without detail": {
			err:    application.ErrInsufficientStock,
			code:   codes.FailedPrecondition,
			reason: "INSUFFICIENT_STOCK",
		},
		"unknown product": {
			err:    &application.UnknownProductSKUError{},
			code:   codes.NotFound,
			reason: "UNKNOWN_PRODUCT_SKU",
		},
		"unknown product without detail": {
			err:    application.ErrUnknownProductSKU,
			code:   codes.NotFound,
			reason: "UNKNOWN_PRODUCT_SKU",
		},
		"idempotency conflict": {
			err:    &application.IdempotencyConflictError{},
			code:   codes.AlreadyExists,
			reason: "IDEMPOTENCY_CONFLICT",
		},
		"a bug on this server": {
			err:    errors.New("a column went missing"),
			code:   codes.Internal,
			reason: "INTERNAL",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newServer(testCase.err).ReserveStock(context.Background(), testRequest())

			require.Error(t, err)
			assert.Equal(t, testCase.code, status.Code(err))
			assert.Equal(t, testCase.reason, errorInfoFrom(t, err).GetReason())
			assert.Equal(t, "inventory.v1", errorInfoFrom(t, err).GetDomain())
		})
	}
}

// The message a caller sees is this server's own wording. Passing the error
// through would publish internal phrasing as though it were contract.
func TestInvalidRequestDoesNotEchoTheInternalMessage(t *testing.T) {
	server := newServer(&application.InvalidRequestError{
		Violations: []application.FieldViolation{{Field: "order_id", Description: "must be a uuid"}},
	})

	_, err := server.ReserveStock(context.Background(), testRequest())

	assert.Equal(t, "the request could not be validated", status.Convert(err).Message())
}

func TestErrorInfoCarriesTheRequestIDWhenTheCallerSentOne(t *testing.T) {
	ctx := correlation.WithRequestID(context.Background(), "probe-123")

	_, err := newServer(application.ErrInsufficientStock).ReserveStock(ctx, testRequest())

	assert.Equal(t, "probe-123", errorInfoFrom(t, err).GetMetadata()["request_id"])
}

// A call without a request id is normal, not an error, and must not produce a
// field that says the id was empty.
func TestErrorInfoOmitsTheRequestIDWhenThereIsNone(t *testing.T) {
	_, err := newServer(application.ErrInsufficientStock).ReserveStock(
		context.Background(),
		testRequest(),
	)

	assert.NotContains(t, errorInfoFrom(t, err).GetMetadata(), "request_id")
}
