package grpcapi_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/adapter/inbound/grpcapi"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

const testOrderID = "018f0f38-5a52-7a01-8000-000000000010"

func TestReserveStockSucceedsWithAnEmptyResponse(t *testing.T) {
	response, err := newServer(nil).ReserveStock(context.Background(), testRequest())

	require.NoError(t, err)
	assert.NotNil(t, response)
}

func TestReserveStockPassesTheRequestToTheUseCase(t *testing.T) {
	useCase := &fakeUseCase{}
	server := grpcapi.NewServer(useCase, zerolog.New(io.Discard))

	_, err := server.ReserveStock(context.Background(), &inventoryv1.ReserveStockRequest{
		OrderId: testOrderID,
		Lines: []*inventoryv1.ReservationLine{
			{ProductSku: "SKU-A", Quantity: 2},
			{ProductSku: "SKU-B", Quantity: 1},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, testOrderID, useCase.command.OrderID)
	assert.Equal(t, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 2},
		{ProductSKU: "SKU-B", Quantity: 1},
	}, useCase.command.Lines)
}

// Nothing about how this server broke may reach a caller.
func TestInternalSaysNothingAboutWhatWentWrong(t *testing.T) {
	server := newServer(errors.New("connection refused to secret-host:5432"))

	_, err := server.ReserveStock(context.Background(), testRequest())

	require.Error(t, err)
	assert.Equal(t, "an unexpected error occurred", status.Convert(err).Message())
	assert.NotContains(t, err.Error(), "secret-host")
	assert.Len(
		t,
		status.Convert(err).Details(),
		1,
		"the ErrorInfo names the failure; nothing else describes it",
	)
}

// --- helpers -------------------------------------------------------------

type fakeUseCase struct {
	err     error
	command application.ReserveStockCommand
}

func (useCase *fakeUseCase) Execute(
	_ context.Context,
	command application.ReserveStockCommand,
) error {
	useCase.command = command

	return useCase.err
}

func newServer(err error) *grpcapi.Server {
	return grpcapi.NewServer(&fakeUseCase{err: err}, zerolog.New(io.Discard))
}

func testRequest() *inventoryv1.ReserveStockRequest {
	return &inventoryv1.ReserveStockRequest{
		OrderId: testOrderID,
		Lines:   []*inventoryv1.ReservationLine{{ProductSku: "SKU-A", Quantity: 2}},
	}
}

func errorInfoFrom(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()

	return detailOf[*errdetails.ErrorInfo](t, err)
}

// detailOf finds the one detail of the wanted type, failing the test when the
// status does not carry it.
func detailOf[T any](t *testing.T, err error) T {
	t.Helper()

	for _, detail := range status.Convert(err).Details() {
		if typed, matches := detail.(T); matches {
			return typed
		}
	}

	var missing T
	require.Failf(t, "missing status detail", "no %T on %v", missing, err)

	return missing
}
