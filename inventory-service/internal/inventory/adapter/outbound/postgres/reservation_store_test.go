//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/platform/database"
)

func TestClaimReservesStockAndRecordsTheRequest(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)
	seedStock(t, ctx, db, map[string]int{"SKU-A": 10, "SKU-B": 4})

	orderID := newUUID(t)
	require.NoError(t, store.Claim(ctx, orderID, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 3},
		{ProductSKU: "SKU-B", Quantity: 1},
	}))

	assert.Equal(t, 3, reservedFor(t, ctx, db, "SKU-A"))
	assert.Equal(t, 1, reservedFor(t, ctx, db, "SKU-B"))

	held, err := store.Find(ctx, orderID)
	require.NoError(t, err)
	assert.Equal(t, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 3},
		{ProductSKU: "SKU-B", Quantity: 1},
	}, held)
}

// The headline invariant. Whatever the interleaving, the warehouse must never
// promise more units than it holds.
func TestConcurrentClaimsNeverOverbookAProduct(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)

	const (
		onHand    = 20
		perOrder  = 3
		attempts  = 30
		expectOK  = onHand / perOrder // 6 orders fit; the rest cannot
		remaining = onHand - (expectOK * perOrder)
	)

	seedStock(t, ctx, db, map[string]int{"SKU-A": onHand})

	reservedOK := make([]bool, attempts)
	errs := make([]error, attempts)

	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	for index := range attempts {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start // release them together, to maximise the overlap

			err := store.Claim(ctx, newUUID(t), []application.Line{
				{ProductSKU: "SKU-A", Quantity: perOrder},
			})
			switch {
			case err == nil:
				reservedOK[index] = true
			case errors.Is(err, application.ErrInsufficientStock):
			default:
				errs[index] = err
			}
		}()
	}
	close(start)
	waitGroup.Wait()

	reserved := 0
	for index := range attempts {
		require.NoError(t, errs[index], "attempt %d failed unexpectedly", index)
		if reservedOK[index] {
			reserved++
		}
	}

	assert.Equal(t, expectOK, reserved, "exactly the orders that fit must succeed")
	assert.Equal(t, expectOK*perOrder, reservedFor(t, ctx, db, "SKU-A"))
	assert.Equal(t, remaining, availableFor(t, ctx, db, "SKU-A"))
	assert.Zero(t, overbookedProducts(t, ctx, db))
}

// Two orders covering the same two products in opposite request order. Without
// sorting the lines before locking, this is a lock cycle and PostgreSQL kills
// one transaction with a deadlock error.
func TestConcurrentClaimsOnOppositeLineOrdersDoNotDeadlock(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)
	seedStock(t, ctx, db, map[string]int{"SKU-A": 500, "SKU-B": 500})

	const attempts = 40

	errs := make([]error, attempts)

	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	for index := range attempts {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start

			lines := []application.Line{
				{ProductSKU: "SKU-A", Quantity: 1},
				{ProductSKU: "SKU-B", Quantity: 1},
			}
			if index%2 == 1 {
				lines[0], lines[1] = lines[1], lines[0]
			}

			// Canonicalize is what imposes the deterministic lock order; the
			// store documents that its input is already canonical.
			_, canonical, err := application.Canonicalize(newUUID(t), lines)
			require.NoError(t, err)

			errs[index] = store.Claim(ctx, newUUID(t), canonical)
		}()
	}
	close(start)
	waitGroup.Wait()

	for index := range attempts {
		require.NoError(t, errs[index], "attempt %d deadlocked or failed", index)
	}

	assert.Equal(t, attempts, reservedFor(t, ctx, db, "SKU-A"))
	assert.Equal(t, attempts, reservedFor(t, ctx, db, "SKU-B"))
}

// Every caller racing on one order id must end up sharing a single reservation,
// and stock must move exactly once.
func TestConcurrentClaimsOnTheSameOrderIDProduceOneReservation(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)
	seedStock(t, ctx, db, map[string]int{"SKU-A": 100})

	const attempts = 16

	orderID := newUUID(t)
	claimed := make([]int, attempts)
	rejectedByRace := make([]bool, attempts)

	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	for index := range attempts {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start

			err := store.Claim(ctx, orderID, []application.Line{
				{ProductSKU: "SKU-A", Quantity: 2},
			})
			switch {
			case err == nil:
				claimed[index] = 1
			case assert.ErrorIs(t, err, application.ErrReservationAlreadyClaimed):
				rejectedByRace[index] = true
			}
		}()
	}
	close(start)
	waitGroup.Wait()

	winners := 0
	for index := range attempts {
		winners += claimed[index]
		require.True(
			t,
			claimed[index] == 1 || rejectedByRace[index],
			"attempt %d neither claimed nor lost the race",
			index,
		)
	}

	assert.Equal(t, 1, winners, "exactly one claim may win")
	assert.Equal(t, 2, reservedFor(t, ctx, db, "SKU-A"), "stock must move once")
	assert.Equal(t, 1, reservationCount(t, ctx, db))
}

// A partly-satisfiable order must leave stock exactly as it found it, while
// still recording why it failed.
func TestClaimRollsBackEarlyLinesWhenALaterLineHasNoStock(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)
	seedStock(t, ctx, db, map[string]int{"SKU-A": 10, "SKU-B": 10, "SKU-C": 1})

	orderID := newUUID(t)
	err := store.Claim(ctx, orderID, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 5},
		{ProductSKU: "SKU-B", Quantity: 5},
		{ProductSKU: "SKU-C", Quantity: 5},
	})
	require.ErrorIs(t, err, application.ErrInsufficientStock)

	// The rollback put back what the first two lines had already taken.
	assert.Equal(t, 0, reservedFor(t, ctx, db, "SKU-A"))
	assert.Equal(t, 0, reservedFor(t, ctx, db, "SKU-B"))
	assert.Equal(t, 0, reservedFor(t, ctx, db, "SKU-C"))

	// And nothing was recorded, so the order id is free to try again.
	assert.Equal(t, 0, reservationCount(t, ctx, db))
	_, err = store.Find(ctx, orderID)
	assert.ErrorIs(t, err, application.ErrReservationNotFound)
}

// The behaviour that motivated dropping stored rejections: an order refused while
// a product was sold out must succeed once the warehouse restocks.
func TestAFailedAttemptCanSucceedAfterRestocking(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)
	seedStock(t, ctx, db, map[string]int{"SKU-A": 1})

	orderID := newUUID(t)
	err := store.Claim(ctx, orderID, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 5},
	})
	require.ErrorIs(t, err, application.ErrInsufficientStock)

	restock(t, ctx, db, "SKU-A", 500)

	require.NoError(t, store.Claim(ctx, orderID, []application.Line{
		{ProductSKU: "SKU-A", Quantity: 5},
	}))
	assert.Equal(t, 5, reservedFor(t, ctx, db, "SKU-A"))
}

func TestUnknownProductIsReportedAndRecordsNothing(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	store := postgres.NewReservationStore(db)
	seedStock(t, ctx, db, map[string]int{"SKU-A": 10})

	err := store.Claim(ctx, newUUID(t), []application.Line{
		{ProductSKU: "SKU-GHOST", Quantity: 1},
	})

	require.ErrorIs(t, err, application.ErrUnknownProductSKU)
	assert.Equal(t, 0, reservedFor(t, ctx, db, "SKU-A"))
	assert.Equal(t, 0, reservationCount(t, ctx, db))
}

func TestFindReportsAMissingReservation(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)

	_, err := postgres.NewReservationStore(db).Find(ctx, newUUID(t))

	require.ErrorIs(t, err, application.ErrReservationNotFound)
}

// --- helpers -------------------------------------------------------------

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
		testpostgres.WithDatabase("inventory"),
		testpostgres.WithUsername("inventory"),
		testpostgres.WithPassword("inventory_test_password"),
		testpostgres.WithInitScripts(upMigrations...),
		testpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := database.New(ctx, database.Config{
		URL: connectionString,
		// Comfortably more than one, so the concurrency tests really do run
		// their transactions at the same time instead of queueing on the pool.
		MaxOpenConns:    20,
		MaxIdleConns:    10,
		ConnMaxLifetime: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	return db
}

func seedStock(t *testing.T, ctx context.Context, db *sql.DB, stock map[string]int) {
	t.Helper()

	for productSKU, onHand := range stock {
		_, err := db.ExecContext(
			ctx,
			`INSERT INTO stock_items (product_sku, on_hand) VALUES ($1, $2)`,
			productSKU,
			onHand,
		)
		require.NoError(t, err)
	}
}

func restock(t *testing.T, ctx context.Context, db *sql.DB, productSKU string, onHand int) {
	t.Helper()

	_, err := db.ExecContext(
		ctx,
		`UPDATE stock_items SET on_hand = $2 WHERE product_sku = $1`,
		productSKU,
		onHand,
	)
	require.NoError(t, err)
}

func reservedFor(t *testing.T, ctx context.Context, db *sql.DB, productSKU string) int {
	t.Helper()

	return scalar(t, ctx, db, `SELECT reserved FROM stock_items WHERE product_sku = $1`, productSKU)
}

func availableFor(t *testing.T, ctx context.Context, db *sql.DB, productSKU string) int {
	t.Helper()

	return scalar(
		t,
		ctx,
		db,
		`SELECT on_hand - reserved FROM stock_items WHERE product_sku = $1`,
		productSKU,
	)
}

// overbookedProducts is the invariant restated as a query. The CHECK constraint
// should make it impossible for this to be anything but zero.
func overbookedProducts(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()

	return scalar(t, ctx, db, `SELECT count(*) FROM stock_items WHERE reserved > on_hand`)
}

func reservationCount(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()

	return scalar(t, ctx, db, `SELECT count(DISTINCT order_id) FROM reservations`)
}

func scalar(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) int {
	t.Helper()

	var value int
	require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&value))

	return value
}

func newUUID(t *testing.T) string {
	t.Helper()

	id, err := uuid.NewV7()
	require.NoError(t, err)

	return id.String()
}
