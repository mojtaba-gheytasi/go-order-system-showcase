package inventorygrpc_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/inventorygrpc"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/correlation"
)

func testRequest() application.ReservationRequest {
	return application.ReservationRequest{
		OrderID: domain.OrderID("018f0f38-5a52-7a01-8000-000000000010"),
		Lines:   []application.ReservationLine{{ProductSKU: "SKU-A", Quantity: 2}},
	}
}

func TestReserveSucceedsWhenInventoryAccepts(t *testing.T) {
	reserver := newReserver(t, &fakeInventory{}, time.Second)

	require.NoError(t, reserver.Reserve(context.Background(), testRequest()))
}

// This table is the retry contract. CreateOrder retries only
// ErrInventoryUnavailable, so what this mapping calls retryable is what actually
// gets retried three times.
func TestReserveMapsStatusCodesOntoApplicationErrors(t *testing.T) {
	tests := map[string]struct {
		code      codes.Code
		want      error
		retryable bool
	}{
		"out of stock": {
			code:      codes.FailedPrecondition,
			want:      application.ErrInsufficientStock,
			retryable: false,
		},
		"unknown product": {
			code:      codes.NotFound,
			want:      inventorygrpc.ErrInventoryRejectedRequest,
			retryable: false,
		},
		"malformed request": {
			code:      codes.InvalidArgument,
			want:      inventorygrpc.ErrInventoryRejectedRequest,
			retryable: false,
		},
		"idempotency conflict": {
			code:      codes.AlreadyExists,
			want:      inventorygrpc.ErrInventoryRejectedRequest,
			retryable: false,
		},
		"inventory unreachable": {
			code:      codes.Unavailable,
			want:      application.ErrInventoryUnavailable,
			retryable: true,
		},
		// A bug on the server. Retrying reproduces it and adds load to a
		// service already in trouble, so this must NOT be retryable.
		"server fault": {
			code:      codes.Internal,
			want:      nil,
			retryable: false,
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			reserver := newReserver(
				t,
				&fakeInventory{err: status.Error(testCase.code, "from the test")},
				time.Second,
			)

			err := reserver.Reserve(context.Background(), testRequest())
			require.Error(t, err)

			if testCase.want != nil {
				require.ErrorIs(t, err, testCase.want)
			}

			assert.Equal(
				t,
				testCase.retryable,
				errors.Is(err, application.ErrInventoryUnavailable),
				"whether CreateOrder will retry this",
			)
		})
	}
}

// The shortfall is what a customer can act on, so it has to survive the trip
// across the wire and back into this service's own vocabulary.
func TestReserveCarriesTheShortfallBackFromInventory(t *testing.T) {
	detailed, err := status.New(codes.FailedPrecondition, "insufficient stock").WithDetails(
		&inventoryv1.InsufficientStockDetail{
			Shortfalls: []*inventoryv1.InsufficientStockDetail_Shortfall{
				{ProductSku: "SKU-A", Requested: 5, Available: 2},
				{ProductSku: "SKU-B", Requested: 3, Available: 0},
			},
		},
	)
	require.NoError(t, err)

	reserver := newReserver(t, &fakeInventory{err: detailed.Err()}, time.Second)

	err = reserver.Reserve(context.Background(), testRequest())

	require.ErrorIs(t, err, application.ErrInsufficientStock, "the retry contract is unchanged")

	var insufficient *application.InsufficientStockError
	require.ErrorAs(t, err, &insufficient)
	assert.Equal(t, []application.Shortfall{
		{ProductSKU: "SKU-A", Requested: 5, Available: 2},
		{ProductSKU: "SKU-B", Requested: 3, Available: 0},
	}, insufficient.Shortfalls)
}

// Details refine the answer; they never decide it. An inventory that sends none
// — an older build, or a failure it has nothing to add about — must behave
// exactly as it did before details existed.
func TestReserveWorksAgainstAnInventoryThatSendsNoDetails(t *testing.T) {
	reserver := newReserver(
		t,
		&fakeInventory{err: status.Error(codes.FailedPrecondition, "insufficient stock")},
		time.Second,
	)

	err := reserver.Reserve(context.Background(), testRequest())

	require.ErrorIs(t, err, application.ErrInsufficientStock)

	var insufficient *application.InsufficientStockError
	assert.False(t, errors.As(err, &insufficient), "nothing to report is not an empty report")
}

// A rejection is this service's bug, not the customer's, so it stays a 500. What
// inventory said about it still has to reach the log an operator will read:
// unknown skus here mean the catalogue and the warehouse disagree.
func TestReserveKeepsWhatInventorySaidAboutARejection(t *testing.T) {
	detailed, err := status.New(codes.NotFound, "unknown product sku").WithDetails(
		&inventoryv1.UnknownProductsDetail{ProductSkus: []string{"SKU-GHOST", "SKU-PHANTOM"}},
	)
	require.NoError(t, err)

	reserver := newReserver(t, &fakeInventory{err: detailed.Err()}, time.Second)

	err = reserver.Reserve(context.Background(), testRequest())

	require.ErrorIs(t, err, inventorygrpc.ErrInventoryRejectedRequest)
	assert.Contains(t, err.Error(), "SKU-GHOST")
	assert.Contains(t, err.Error(), "SKU-PHANTOM")
}

// A hung inventory has to surface as the retryable case, not hang the order
// request until the HTTP write timeout fires.
func TestReserveTurnsAStalledCallIntoARetryableError(t *testing.T) {
	reserver := newReserver(t, &fakeInventory{delay: time.Second}, 50*time.Millisecond)

	err := reserver.Reserve(context.Background(), testRequest())

	require.ErrorIs(t, err, application.ErrInventoryUnavailable)
}

// Inventory's INTERNAL says only that it is broken, never how. The request id is
// the thread from a customer's failed order to the log line over there that
// explains it, so it has to arrive on the call.
func TestReserveSendsTheRequestIDToInventory(t *testing.T) {
	inventory := &fakeInventory{}
	reserver := newReserver(t, inventory, time.Second)

	ctx := correlation.WithRequestID(context.Background(), "probe-123")
	require.NoError(t, reserver.Reserve(ctx, testRequest()))

	assert.Equal(t, []string{"probe-123"}, inventory.receivedRequestIDs())
}

// A call without an id is normal. It must not put an empty one on the wire,
// which would be indistinguishable from a caller that sent a blank id.
func TestReserveSendsNoRequestIDWhenThereIsNone(t *testing.T) {
	inventory := &fakeInventory{}
	reserver := newReserver(t, inventory, time.Second)

	require.NoError(t, reserver.Reserve(context.Background(), testRequest()))

	assert.Empty(t, inventory.receivedRequestIDs())
}

// --- helpers -------------------------------------------------------------

func newReserver(
	t *testing.T,
	inventory inventoryv1.InventoryServiceServer,
	timeout time.Duration,
) *inventorygrpc.Reserver {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	inventoryv1.RegisterInventoryServiceServer(server, inventory)

	go func() {
		_ = server.Serve(listener)
	}()

	connection, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// The same interceptor bootstrap registers, so these tests exercise the
		// connection the process actually makes.
		grpc.WithChainUnaryInterceptor(inventorygrpc.Correlation()),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	})

	return inventorygrpc.NewReserver(connection, timeout)
}

type fakeInventory struct {
	inventoryv1.UnimplementedInventoryServiceServer

	err   error
	delay time.Duration

	mutex      sync.Mutex
	requestIDs []string
}

// receivedRequestIDs reports the correlation ids that arrived in call metadata.
func (inventory *fakeInventory) receivedRequestIDs() []string {
	inventory.mutex.Lock()
	defer inventory.mutex.Unlock()

	return slices.Clone(inventory.requestIDs)
}

func (inventory *fakeInventory) recordRequestID(ctx context.Context) {
	incoming, found := metadata.FromIncomingContext(ctx)
	if found == false {
		return
	}

	inventory.mutex.Lock()
	defer inventory.mutex.Unlock()
	inventory.requestIDs = append(inventory.requestIDs, incoming.Get(correlation.MetadataKey)...)
}

func (inventory *fakeInventory) ReserveStock(
	ctx context.Context,
	_ *inventoryv1.ReserveStockRequest,
) (*inventoryv1.ReserveStockResponse, error) {
	inventory.recordRequestID(ctx)

	if inventory.delay > 0 {
		select {
		case <-time.After(inventory.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if inventory.err != nil {
		return nil, inventory.err
	}

	return &inventoryv1.ReserveStockResponse{}, nil
}
