//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
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
	require.NoError(t, accepted.Accept(
		"018f0f38-5a52-7a01-8000-000000000030",
		accepted.CreatedAt().Add(time.Minute),
	))
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
	assert.Equal(t, expected.ReservationID(), actual.ReservationID())
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
