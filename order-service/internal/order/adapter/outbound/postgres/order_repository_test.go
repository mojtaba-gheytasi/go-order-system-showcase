//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/correlation"
	platformdatabase "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/database"
)

func TestOrderRepositoryCreateAndFind(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	repository := postgres.NewOrderRepository(db)

	created := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "create-order-1")
	require.NoError(t, repository.Create(ctx, created))

	foundByID, err := repository.FindByID(ctx, created.ID())
	require.NoError(t, err)
	assertOrdersEqual(t, created, foundByID)

	foundByKey, err := repository.FindByIdempotencyKey(ctx, created.IdempotencyKey())
	require.NoError(t, err)
	assertOrdersEqual(t, created, foundByKey)
}

func TestOrderRepositoryConditionallyUpdatesAcceptedAndRejectedOrders(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	repository := postgres.NewOrderRepository(db)

	accepted := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "accept-order")
	require.NoError(t, repository.Create(ctx, accepted))
	require.NoError(t, accepted.Accept(accepted.CreatedAt().Add(time.Minute)))
	require.NoError(t, repository.Update(ctx, accepted, domain.StatusPending))

	foundAccepted, err := repository.FindByID(ctx, accepted.ID())
	require.NoError(t, err)
	assertOrdersEqual(t, accepted, foundAccepted)

	// The expected pending state no longer matches, so a competing update
	// cannot overwrite the transition that already won.
	err = repository.Update(ctx, accepted, domain.StatusPending)
	require.ErrorIs(t, err, application.ErrOrderStateConflict)

	rejected := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000011", "reject-order")
	require.NoError(t, repository.Create(ctx, rejected))
	require.NoError(t, rejected.Reject(rejected.CreatedAt().Add(time.Minute)))
	require.NoError(t, repository.Update(ctx, rejected, domain.StatusPending))

	foundRejected, err := repository.FindByID(ctx, rejected.ID())
	require.NoError(t, err)
	assertOrdersEqual(t, rejected, foundRejected)
}

func TestOrderRepositoryTranslatesDuplicateIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	repository := postgres.NewOrderRepository(db)

	first := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "same-key")
	second := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000011", "same-key")

	require.NoError(t, repository.Create(ctx, first))
	err := repository.Create(ctx, second)

	require.ErrorIs(t, err, application.ErrDuplicateIdempotencyKey)
}

func TestOrderRepositoryAcceptsAnOrderAndRecordsItsEventTogether(t *testing.T) {
	db := startPostgres(t, context.Background())
	repository := postgres.NewOrderRepository(db)
	ctx := correlation.WithRequestID(context.Background(), "request-1")

	order := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "accept-order")
	require.NoError(t, repository.Create(ctx, order))
	require.NoError(t, order.Accept(order.CreatedAt().Add(time.Minute)))

	err := repository.UpdateAndRecordEvent(
		ctx,
		order,
		domain.StatusPending,
		acceptedEventFor(order, "018f0f38-5a52-7a01-8000-0000000000e1"),
	)
	require.NoError(t, err)

	stored, err := repository.FindByID(ctx, order.ID())
	require.NoError(t, err)
	assert.Equal(t, domain.StatusAccepted, stored.Status())

	var orderID, correlationID, status string
	err = db.QueryRowContext(ctx, `
		SELECT order_id, correlation_id, status
		  FROM outbox_events
		 WHERE event_id = '018f0f38-5a52-7a01-8000-0000000000e1'`,
	).Scan(&orderID, &correlationID, &status)
	require.NoError(t, err)
	assert.Equal(t, string(order.ID()), orderID)
	assert.Equal(t, "request-1", correlationID)
	assert.Equal(t, "pending", status)
}

func TestOrderRepositoryRecordsNoEventWhenTheStateHasMoved(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	repository := postgres.NewOrderRepository(db)

	order := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "accept-order")
	require.NoError(t, repository.Create(ctx, order))
	require.NoError(t, order.Accept(order.CreatedAt().Add(time.Minute)))
	require.NoError(t, repository.Update(ctx, order, domain.StatusPending))

	err := repository.UpdateAndRecordEvent(
		ctx,
		order,
		domain.StatusPending,
		acceptedEventFor(order, "018f0f38-5a52-7a01-8000-0000000000e1"),
	)

	require.ErrorIs(t, err, application.ErrOrderStateConflict)
	assert.Equal(t, 0, countOutboxEvents(t, db))
}

// The proof that the two writes are one transaction: the state change succeeds,
// the event insert fails after it, and the state change must not survive.
func TestOrderRepositoryRollsBackTheAcceptanceWhenTheEventCannotBeStored(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	repository := postgres.NewOrderRepository(db)
	const sharedEventID = "018f0f38-5a52-7a01-8000-0000000000e1"

	first := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "first-order")
	require.NoError(t, repository.Create(ctx, first))
	require.NoError(t, first.Accept(first.CreatedAt().Add(time.Minute)))
	require.NoError(t, repository.UpdateAndRecordEvent(
		ctx, first, domain.StatusPending, acceptedEventFor(first, sharedEventID),
	))

	second := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000011", "second-order")
	require.NoError(t, repository.Create(ctx, second))
	require.NoError(t, second.Accept(second.CreatedAt().Add(time.Minute)))

	err := repository.UpdateAndRecordEvent(
		ctx, second, domain.StatusPending, acceptedEventFor(second, sharedEventID),
	)
	require.Error(t, err, "the event id is already taken")

	stored, err := repository.FindByID(ctx, second.ID())
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, stored.Status(), "the acceptance rolled back with the event")
	assert.Equal(t, 1, countOutboxEvents(t, db))
}

func TestOrderRepositoryRecordsOneEventWhenTwoAcceptancesRace(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	repository := postgres.NewOrderRepository(db)

	order := newTestOrder(t, "018f0f38-5a52-7a01-8000-000000000010", "raced-order")
	require.NoError(t, repository.Create(ctx, order))

	eventIDs := []string{
		"018f0f38-5a52-7a01-8000-0000000000e1",
		"018f0f38-5a52-7a01-8000-0000000000e2",
	}
	results := make([]error, len(eventIDs))

	// Each racer loads its own copy while the order is still pending, as two
	// requests would, and only then are both released to write.
	racers := make([]*domain.Order, len(eventIDs))
	for index := range racers {
		racer, err := repository.FindByID(ctx, order.ID())
		require.NoError(t, err)
		require.NoError(t, racer.Accept(racer.CreatedAt().Add(time.Minute)))
		racers[index] = racer
	}

	var start, finished sync.WaitGroup
	start.Add(1)
	for index, eventID := range eventIDs {
		finished.Add(1)
		go func() {
			defer finished.Done()
			start.Wait()

			results[index] = repository.UpdateAndRecordEvent(
				ctx, racers[index], domain.StatusPending, acceptedEventFor(racers[index], eventID),
			)
		}()
	}
	start.Done()
	finished.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		assert.ErrorIs(t, err, application.ErrOrderStateConflict)
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, countOutboxEvents(t, db))
}

func acceptedEventFor(order *domain.Order, eventID string) application.OrderAcceptedEvent {
	lines := make([]application.OrderAcceptedLine, 0)
	for _, item := range order.OrderItems() {
		lines = append(lines, application.OrderAcceptedLine{
			ProductSKU: item.ProductSKU(),
			Quantity:   item.Quantity(),
			UnitPrice:  item.UnitPrice(),
		})
	}

	return application.OrderAcceptedEvent{
		EventID:       eventID,
		OrderID:       order.ID(),
		OccurredAt:    order.UpdatedAt(),
		CustomerEmail: order.CustomerEmail(),
		Total:         order.Total(),
		Lines:         lines,
	}
}

func countOutboxEvents(t *testing.T, db *sql.DB) int {
	t.Helper()

	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM outbox_events`).Scan(&count))

	return count
}

func startPostgres(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	migrationsDirectory, err := filepath.Abs("../../../../../migrations")
	require.NoError(t, err)

	upMigrations, err := filepath.Glob(filepath.Join(migrationsDirectory, "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, upMigrations)
	sort.Strings(upMigrations)

	container, err := testpostgres.Run(
		ctx,
		"postgres:18.6-alpine",
		testpostgres.WithDatabase("orders"),
		testpostgres.WithUsername("orders"),
		testpostgres.WithPassword("orders_test_password"),
		testpostgres.WithInitScripts(upMigrations...),
		testpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := platformdatabase.New(ctx, platformdatabase.Config{
		URL:             connectionString,
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	return db
}

func newTestOrder(
	t *testing.T,
	orderID domain.OrderID,
	idempotencyKey string,
) *domain.Order {
	t.Helper()

	firstOrderItem, err := domain.NewOrderItem(
		"SKU-A",
		2,
		domain.Money{AmountInCents: 1250, Currency: "EUR"},
	)
	require.NoError(t, err)

	secondOrderItem, err := domain.NewOrderItem(
		"SKU-B",
		1,
		domain.Money{AmountInCents: 500, Currency: "EUR"},
	)
	require.NoError(t, err)

	order, err := domain.NewOrder(
		orderID,
		"018f0f38-5a52-7a01-8000-000000000020",
		"customer@example.com",
		idempotencyKey,
		[]domain.OrderItem{firstOrderItem, secondOrderItem},
		time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)

	return order
}

func assertOrdersEqual(t *testing.T, expected, actual *domain.Order) {
	t.Helper()

	assert.Equal(t, expected.ID(), actual.ID())
	assert.Equal(t, expected.CustomerID(), actual.CustomerID())
	assert.Equal(t, expected.CustomerEmail(), actual.CustomerEmail())
	assert.Equal(t, expected.Status(), actual.Status())
	assert.Equal(t, expected.IdempotencyKey(), actual.IdempotencyKey())
	assert.Equal(t, expected.Total(), actual.Total())
	assert.True(t, expected.CreatedAt().Equal(actual.CreatedAt()))
	assert.True(t, expected.UpdatedAt().Equal(actual.UpdatedAt()))

	// Order matters: an order item's position is its persisted line number.
	assert.Equal(
		t,
		orderItemSnapshots(expected.OrderItems()),
		orderItemSnapshots(actual.OrderItems()),
	)
}

type orderItemSnapshot struct {
	ProductSKU string
	Quantity   int
	UnitPrice  domain.Money
}

func orderItemSnapshots(orderItems []domain.OrderItem) []orderItemSnapshot {
	snapshots := make([]orderItemSnapshot, 0, len(orderItems))
	for _, orderItem := range orderItems {
		snapshots = append(snapshots, orderItemSnapshot{
			ProductSKU: orderItem.ProductSKU(),
			Quantity:   orderItem.Quantity(),
			UnitPrice:  orderItem.UnitPrice(),
		})
	}

	return snapshots
}
