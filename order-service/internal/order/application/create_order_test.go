package application_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

var (
	fixedNow           = time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	generatedID        = "018f0f38-5a52-7a01-8000-0000000000aa"
	testCustomerID     = "018f0f38-5a52-7a01-8000-000000000020"
	testIdempotencyKey = "create-order-1"
)

func TestCreateOrderPersistsPendingBeforeInventoryThenAcceptsAndPublishes(t *testing.T) {
	dependencies := newDependencies()

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.NoError(t, err)

	require.True(t, result.Created)
	order := result.Order
	assert.Equal(t, domain.OrderID(generatedID), order.ID())
	assert.Equal(t, domain.StatusAccepted, order.Status())
	assert.Equal(t, domain.Money{AmountInCents: 3000, Currency: "EUR"}, order.Total())
	assert.Equal(t, fixedNow, order.CreatedAt())
	assert.Equal(t, domain.StatusPending, dependencies.repository.statusAtCreate)
	assert.Equal(t, domain.StatusAccepted, dependencies.repository.statusAtUpdate)
	assert.Equal(t, domain.StatusPending, dependencies.repository.expectedStatus)
	assert.Equal(t, 1, dependencies.repository.createCalls)
	assert.Equal(t, 1, dependencies.repository.updateCalls)
	assert.Equal(t, 1, dependencies.events.calls)

	created := dependencies.trace.index("create")
	reserved := dependencies.trace.index("reserve")
	require.NotEqual(t, -1, created)
	require.NotEqual(t, -1, reserved)
	assert.Less(t, created, reserved, "pending must be persisted before inventory is called")
	assert.Less(t, dependencies.trace.index("update"), dependencies.trace.index("publish"))

	require.Len(t, dependencies.reserver.requests, 1)
	request := dependencies.reserver.requests[0]
	assert.Equal(t, domain.OrderID(generatedID), request.OrderID)
	assert.Equal(t, []application.ReservationLine{
		{ProductSKU: "SKU-A", Quantity: 2},
		{ProductSKU: "SKU-B", Quantity: 1},
	}, request.Lines)
}

func TestCreateOrderUsesCatalogPrices(t *testing.T) {
	dependencies := newDependencies()
	dependencies.catalog.prices["SKU-A"] = domain.Money{
		AmountInCents: 2000,
		Currency:      "EUR",
	}

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.NoError(t, err)

	assert.Equal(t, domain.Money{AmountInCents: 4500, Currency: "EUR"}, result.Order.Total())
}

func TestCreateOrderReturnsAcceptedReplayWithoutExternalCalls(t *testing.T) {
	dependencies := newDependencies()
	existing := acceptedOrder(t, domain.OrderID(generatedID), testIdempotencyKey, "SKU-A")
	dependencies.repository.findByKey = func(context.Context, string) (*domain.Order, error) {
		return existing, nil
	}

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.NoError(t, err)

	assert.False(t, result.Created)
	assert.Same(t, existing, result.Order)
	assert.Equal(t, 0, dependencies.catalog.calls)
	assert.Equal(t, 0, dependencies.reserver.calls)
	assert.Equal(t, 0, dependencies.repository.createCalls)
	assert.Equal(t, 0, dependencies.repository.updateCalls)
	assert.Equal(t, 0, dependencies.events.calls)
}

func TestCreateOrderReturnsRejectedReplayWithoutExternalCalls(t *testing.T) {
	dependencies := newDependencies()
	existing := pendingOrder(t, domain.OrderID(generatedID), testIdempotencyKey, "SKU-A")
	require.NoError(t, existing.Reject(fixedNow))
	dependencies.repository.findByKey = func(context.Context, string) (*domain.Order, error) {
		return existing, nil
	}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.ErrorIs(t, err, application.ErrInsufficientStock)

	assert.Equal(t, 0, dependencies.catalog.calls)
	assert.Equal(t, 0, dependencies.reserver.calls)
	assert.Equal(t, 0, dependencies.repository.updateCalls)
	assert.Equal(t, 0, dependencies.events.calls)
}

func TestCreateOrderPendingReplayUsesPersistedOrderSnapshot(t *testing.T) {
	dependencies := newDependencies()
	existing := pendingOrder(t, "018f0f38-5a52-7a01-8000-0000000000cc", testIdempotencyKey, "PERSISTED-SKU")
	dependencies.repository.findByKey = func(context.Context, string) (*domain.Order, error) {
		return existing, nil
	}

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.NoError(t, err)

	assert.False(t, result.Created)
	assert.Equal(t, domain.StatusAccepted, result.Order.Status())
	assert.Equal(t, 0, dependencies.catalog.calls, "a replay must not resolve prices again")
	assert.Equal(t, 0, dependencies.repository.createCalls)
	require.Len(t, dependencies.reserver.requests, 1)
	assert.Equal(t, existing.ID(), dependencies.reserver.requests[0].OrderID)
	assert.Equal(t, []application.ReservationLine{
		{ProductSKU: "PERSISTED-SKU", Quantity: 1},
	}, dependencies.reserver.requests[0].Lines)
}

func TestCreateOrderConcurrentDuplicateProcessesTheWinningOrder(t *testing.T) {
	dependencies := newDependencies()
	winner := pendingOrder(t, "018f0f38-5a52-7a01-8000-0000000000cc", testIdempotencyKey, "WINNER-SKU")

	lookups := 0
	dependencies.repository.findByKey = func(context.Context, string) (*domain.Order, error) {
		lookups++
		if lookups == 1 {
			return nil, application.ErrOrderNotFound
		}

		return winner, nil
	}
	dependencies.repository.create = func(context.Context, *domain.Order) error {
		return application.ErrDuplicateIdempotencyKey
	}

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.NoError(t, err)

	assert.False(t, result.Created)
	assert.Same(t, winner, result.Order)
	assert.Equal(t, 2, lookups)
	require.Len(t, dependencies.reserver.requests, 1)
	assert.Equal(t, winner.ID(), dependencies.reserver.requests[0].OrderID)
	assert.Equal(t, "WINNER-SKU", dependencies.reserver.requests[0].Lines[0].ProductSKU)
}

func TestCreateOrderRetriesUnavailableInventoryUntilItSucceeds(t *testing.T) {
	tests := map[string][]error{
		"attempt two": {application.ErrInventoryUnavailable, nil},
		"attempt three": {
			application.ErrInventoryUnavailable,
			application.ErrInventoryUnavailable,
			nil,
		},
	}

	for name, results := range tests {
		t.Run(name, func(t *testing.T) {
			dependencies := newDependencies()
			dependencies.reserver.results = results

			result, err := dependencies.useCase().Execute(context.Background(), testCommand())
			require.NoError(t, err)

			assert.Equal(t, domain.StatusAccepted, result.Order.Status())
			assert.Equal(t, len(results), dependencies.reserver.calls)
			for _, request := range dependencies.reserver.requests {
				assert.Equal(t, domain.OrderID(generatedID), request.OrderID)
				assert.Equal(t, dependencies.reserver.requests[0].Lines, request.Lines)
			}
		})
	}
}

func TestCreateOrderStopsAfterThreeUnavailableInventoryAttemptsAndLeavesPending(t *testing.T) {
	dependencies := newDependencies()
	dependencies.reserver.results = []error{
		application.ErrInventoryUnavailable,
		application.ErrInventoryUnavailable,
		application.ErrInventoryUnavailable,
	}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.ErrorIs(t, err, application.ErrInventoryUnavailable)

	assert.Equal(t, 3, dependencies.reserver.calls)
	assert.Equal(t, domain.StatusPending, dependencies.repository.statusAtCreate)
	assert.Equal(t, 0, dependencies.repository.updateCalls)
	assert.Equal(t, 0, dependencies.events.calls)
}

func TestCreateOrderDoesNotRetryInsufficientStockAndPersistsRejected(t *testing.T) {
	dependencies := newDependencies()
	dependencies.reserver.results = []error{application.ErrInsufficientStock}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.ErrorIs(t, err, application.ErrInsufficientStock)

	assert.Equal(t, 1, dependencies.reserver.calls)
	assert.Equal(t, 1, dependencies.repository.updateCalls)
	assert.Equal(t, domain.StatusRejected, dependencies.repository.statusAtUpdate)
	assert.Equal(t, domain.StatusPending, dependencies.repository.expectedStatus)
	assert.Equal(t, 0, dependencies.events.calls)
}

// The order is stored as rejected either way, but what inventory said about the
// refusal is the only thing a customer can act on, so it must survive being
// persisted rather than being flattened into the sentinel.
func TestCreateOrderKeepsTheShortfallBehindARejection(t *testing.T) {
	dependencies := newDependencies()
	dependencies.reserver.results = []error{&application.InsufficientStockError{
		Shortfalls: []application.Shortfall{{ProductSKU: "SKU-A", Requested: 5, Available: 2}},
	}}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())

	require.ErrorIs(t, err, application.ErrInsufficientStock)

	var insufficient *application.InsufficientStockError
	require.ErrorAs(t, err, &insufficient)
	assert.Equal(t, []application.Shortfall{
		{ProductSKU: "SKU-A", Requested: 5, Available: 2},
	}, insufficient.Shortfalls)
	assert.Equal(t, domain.StatusRejected, dependencies.repository.statusAtUpdate)
}

// A repeat of an order already refused is answered from this service's own
// database, which holds the verdict but not the shortfall behind it. Replaying
// one would state stock levels from an earlier moment as current fact.
func TestCreateOrderRepeatingARejectedOrderReportsNoShortfall(t *testing.T) {
	dependencies := newDependencies()
	existing := pendingOrder(t, domain.OrderID(generatedID), testIdempotencyKey, "SKU-A")
	require.NoError(t, existing.Reject(fixedNow))
	dependencies.repository.findByKey = func(context.Context, string) (*domain.Order, error) {
		return existing, nil
	}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())

	require.ErrorIs(t, err, application.ErrInsufficientStock)

	var insufficient *application.InsufficientStockError
	assert.False(t, errors.As(err, &insufficient), "the verdict without a stale shortfall")
	assert.Equal(t, 0, dependencies.reserver.calls, "inventory is not asked again")
}

func TestCreateOrderContextCancellationStopsInventoryRetryWait(t *testing.T) {
	dependencies := newDependencies()
	dependencies.reserver.results = []error{application.ErrInventoryUnavailable}
	ctx, cancel := context.WithCancel(context.Background())
	dependencies.reserver.afterReserve = cancel

	_, err := dependencies.useCase().Execute(ctx, testCommand())
	require.ErrorIs(t, err, context.Canceled)

	assert.Equal(t, 1, dependencies.reserver.calls)
	assert.Equal(t, 0, dependencies.repository.updateCalls)
}

func TestCreateOrderAttemptsStatePersistenceOnlyOnce(t *testing.T) {
	tests := map[string]error{
		"acceptance": nil,
		"rejection":  application.ErrInsufficientStock,
	}

	for name, inventoryError := range tests {
		t.Run(name, func(t *testing.T) {
			dependencies := newDependencies()
			dependencies.reserver.results = []error{inventoryError}
			databaseError := errors.New("database unavailable")
			dependencies.repository.update = func(
				context.Context,
				*domain.Order,
				domain.Status,
			) error {
				return databaseError
			}

			_, err := dependencies.useCase().Execute(context.Background(), testCommand())
			require.ErrorIs(t, err, databaseError)
			assert.Equal(t, 1, dependencies.repository.updateCalls)
			assert.Equal(t, 0, dependencies.events.calls)
		})
	}
}

func TestCreateOrderReloadsTheWinningStateAfterAConflict(t *testing.T) {
	dependencies := newDependencies()
	winner := acceptedOrder(t, domain.OrderID(generatedID), testIdempotencyKey, "SKU-A")
	dependencies.repository.update = func(context.Context, *domain.Order, domain.Status) error {
		return application.ErrOrderStateConflict
	}
	dependencies.repository.findByID = func(context.Context, domain.OrderID) (*domain.Order, error) {
		return winner, nil
	}

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())
	require.NoError(t, err)

	assert.Same(t, winner, result.Order)
	assert.Equal(t, 1, dependencies.repository.updateCalls)
	assert.Equal(t, 1, dependencies.repository.findByIDCalls)
	assert.Equal(t, 0, dependencies.events.calls, "only the request that stores acceptance publishes")
}

func TestCreateOrderPropagatesCatalogFailures(t *testing.T) {
	tests := map[string]error{
		"unknown product": application.ErrProductNotFound,
		"unavailable":     application.ErrCatalogUnavailable,
	}

	for name, catalogError := range tests {
		t.Run(name, func(t *testing.T) {
			dependencies := newDependencies()
			dependencies.catalog.err = catalogError

			_, err := dependencies.useCase().Execute(context.Background(), testCommand())

			require.ErrorIs(t, err, catalogError)
			assert.Equal(t, 0, dependencies.reserver.calls)
			assert.Equal(t, 0, dependencies.repository.createCalls)
		})
	}
}

func TestCreateOrderRejectsAProductMissingFromTheCatalogResponse(t *testing.T) {
	dependencies := newDependencies()
	delete(dependencies.catalog.prices, "SKU-B")

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())

	require.ErrorIs(t, err, application.ErrProductNotFound)
	assert.Equal(t, 0, dependencies.reserver.calls)

	var notFound *application.ProductNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, []string{"SKU-B"}, notFound.ProductSKUs)
}

// Naming only the first unpriced product would cost a customer one attempt per
// bad sku, so all of them are collected before giving up.
func TestCreateOrderNamesEveryProductTheCatalogCouldNotPrice(t *testing.T) {
	dependencies := newDependencies()
	dependencies.catalog.prices = map[string]domain.Money{}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())

	var notFound *application.ProductNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, []string{"SKU-A", "SKU-B"}, notFound.ProductSKUs)
}

func TestCreateOrderDoesNotCallInventoryWhenInitialPersistenceFails(t *testing.T) {
	dependencies := newDependencies()
	failure := errors.New("connection reset")
	dependencies.repository.create = func(context.Context, *domain.Order) error {
		return failure
	}

	_, err := dependencies.useCase().Execute(context.Background(), testCommand())

	require.ErrorIs(t, err, failure)
	assert.Equal(t, 0, dependencies.reserver.calls)
	assert.Equal(t, 0, dependencies.events.calls)
}

func TestCreateOrderSucceedsWhenPublicationFails(t *testing.T) {
	dependencies := newDependencies()
	dependencies.events.err = errors.New("broker unavailable")

	result, err := dependencies.useCase().Execute(context.Background(), testCommand())

	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Equal(t, domain.StatusAccepted, result.Order.Status())
	assert.Equal(t, 1, dependencies.events.calls)
}

func TestCreateOrderRejectsAnInvalidCommandBeforePersistence(t *testing.T) {
	dependencies := newDependencies()
	command := testCommand()
	command.CustomerEmail = "not-an-email"

	_, err := dependencies.useCase().Execute(context.Background(), command)

	require.ErrorIs(t, err, domain.ErrInvalidOrder)
	assert.Equal(t, 0, dependencies.reserver.calls)
	assert.Equal(t, 0, dependencies.repository.createCalls)
}

func testCommand() application.CreateOrderCommand {
	return application.CreateOrderCommand{
		IdempotencyKey: testIdempotencyKey,
		CustomerID:     testCustomerID,
		CustomerEmail:  "customer@example.com",
		Items: []application.CreateOrderItem{
			{ProductSKU: "SKU-A", Quantity: 2},
			{ProductSKU: "SKU-B", Quantity: 1},
		},
	}
}

func pendingOrder(
	t *testing.T,
	orderID domain.OrderID,
	idempotencyKey string,
	productSKU string,
) *domain.Order {
	t.Helper()

	orderItem, err := domain.NewOrderItem(
		productSKU,
		1,
		domain.Money{AmountInCents: 1000, Currency: "EUR"},
	)
	require.NoError(t, err)

	order, err := domain.NewOrder(
		orderID,
		domain.CustomerID(testCustomerID),
		"customer@example.com",
		idempotencyKey,
		[]domain.OrderItem{orderItem},
		fixedNow,
	)
	require.NoError(t, err)

	return order
}

func acceptedOrder(
	t *testing.T,
	orderID domain.OrderID,
	idempotencyKey string,
	productSKU string,
) *domain.Order {
	t.Helper()

	order := pendingOrder(t, orderID, idempotencyKey, productSKU)
	require.NoError(t, order.Accept(fixedNow))

	return order
}

type callTrace struct{ calls []string }

func (trace *callTrace) record(name string) { trace.calls = append(trace.calls, name) }

func (trace *callTrace) index(name string) int {
	for index, call := range trace.calls {
		if call == name {
			return index
		}
	}

	return -1
}

type dependencies struct {
	repository *fakeRepository
	catalog    *fakeCatalog
	reserver   *fakeReserver
	events     *fakeEventPublisher
	trace      *callTrace
}

func newDependencies() *dependencies {
	trace := &callTrace{}

	return &dependencies{
		repository: &fakeRepository{trace: trace},
		catalog: &fakeCatalog{prices: map[string]domain.Money{
			"SKU-A": {AmountInCents: 1250, Currency: "EUR"},
			"SKU-B": {AmountInCents: 500, Currency: "EUR"},
		}},
		reserver: &fakeReserver{trace: trace},
		events:   &fakeEventPublisher{trace: trace},
		trace:    trace,
	}
}

func (d *dependencies) useCase() *application.CreateOrder {
	return application.NewCreateOrder(
		d.repository,
		d.catalog,
		d.reserver,
		d.events,
		func() time.Time { return fixedNow },
		func() string { return generatedID },
		zerolog.New(io.Discard),
	)
}

type fakeCatalog struct {
	prices map[string]domain.Money
	err    error
	calls  int
}

var _ application.ProductCatalog = (*fakeCatalog)(nil)

func (catalog *fakeCatalog) Prices(
	context.Context,
	[]string,
) (map[string]domain.Money, error) {
	catalog.calls++

	if catalog.err != nil {
		return nil, catalog.err
	}

	return catalog.prices, nil
}

type fakeRepository struct {
	trace          *callTrace
	create         func(context.Context, *domain.Order) error
	update         func(context.Context, *domain.Order, domain.Status) error
	findByID       func(context.Context, domain.OrderID) (*domain.Order, error)
	findByKey      func(context.Context, string) (*domain.Order, error)
	createCalls    int
	updateCalls    int
	findByIDCalls  int
	statusAtCreate domain.Status
	statusAtUpdate domain.Status
	expectedStatus domain.Status
}

var _ application.OrderRepository = (*fakeRepository)(nil)

func (repository *fakeRepository) Create(ctx context.Context, order *domain.Order) error {
	repository.trace.record("create")
	repository.createCalls++
	repository.statusAtCreate = order.Status()

	if repository.create != nil {
		return repository.create(ctx, order)
	}

	return nil
}

func (repository *fakeRepository) Update(
	ctx context.Context,
	order *domain.Order,
	expectedStatus domain.Status,
) error {
	repository.trace.record("update")
	repository.updateCalls++
	repository.statusAtUpdate = order.Status()
	repository.expectedStatus = expectedStatus

	if repository.update != nil {
		return repository.update(ctx, order, expectedStatus)
	}

	return nil
}

func (repository *fakeRepository) FindByID(
	ctx context.Context,
	orderID domain.OrderID,
) (*domain.Order, error) {
	repository.trace.record("find_by_id")
	repository.findByIDCalls++

	if repository.findByID != nil {
		return repository.findByID(ctx, orderID)
	}

	return nil, application.ErrOrderNotFound
}

func (repository *fakeRepository) FindByIdempotencyKey(
	ctx context.Context,
	key string,
) (*domain.Order, error) {
	repository.trace.record("find_by_key")

	if repository.findByKey != nil {
		return repository.findByKey(ctx, key)
	}

	return nil, application.ErrOrderNotFound
}

type fakeReserver struct {
	trace        *callTrace
	results      []error
	requests     []application.ReservationRequest
	afterReserve func()
	calls        int
}

var _ application.InventoryReserver = (*fakeReserver)(nil)

func (reserver *fakeReserver) Reserve(
	_ context.Context,
	request application.ReservationRequest,
) error {
	reserver.trace.record("reserve")
	reserver.calls++
	reserver.requests = append(reserver.requests, request)
	if reserver.afterReserve != nil {
		reserver.afterReserve()
	}

	resultIndex := reserver.calls - 1
	if resultIndex < len(reserver.results) && reserver.results[resultIndex] != nil {
		return reserver.results[resultIndex]
	}

	return nil
}

type fakeEventPublisher struct {
	trace *callTrace
	err   error
	calls int

	// published keeps what was announced, not merely that something was. The
	// event is a published contract, so its contents are worth asserting on.
	published []application.OrderAcceptedEvent
}

var _ application.OrderEventPublisher = (*fakeEventPublisher)(nil)

func (publisher *fakeEventPublisher) PublishOrderAccepted(
	_ context.Context,
	event application.OrderAcceptedEvent,
) error {
	publisher.trace.record("publish")
	publisher.calls++
	publisher.published = append(publisher.published, event)

	return publisher.err
}
