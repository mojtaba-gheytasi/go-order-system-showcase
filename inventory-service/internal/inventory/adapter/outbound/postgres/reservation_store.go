package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

// raised when a reservation names a product that does not exist in stock_items
const foreignKeyViolation = "23503"

type ReservationStore struct {
	db     *sql.DB
	logger zerolog.Logger
}

var _ application.ReservationStore = (*ReservationStore)(nil)

func NewReservationStore(db *sql.DB, logger zerolog.Logger) *ReservationStore {
	return &ReservationStore{db: db, logger: logger}
}

func (store *ReservationStore) Find(
	ctx context.Context,
	orderID string,
) ([]application.Line, error) {
	// ORDER BY is not decoration: a primary key constrains storage, not the
	// order rows come back in. The comparison that decides whether a repeated
	// order id asks for the same products depends on this being canonical.
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT product_sku, quantity
		   FROM reservations
		  WHERE order_id = $1
		  ORDER BY product_sku`,
		orderID,
	)
	if err != nil {
		return nil, fmt.Errorf("query reservation: %w", err)
	}
	defer rows.Close()

	lines := make([]application.Line, 0)
	for rows.Next() {
		var line application.Line
		if err := rows.Scan(&line.ProductSKU, &line.Quantity); err != nil {
			return nil, fmt.Errorf("scan reservation line: %w", err)
		}

		lines = append(lines, line)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reservation: %w", err)
	}

	if len(lines) == 0 {
		return nil, application.ErrReservationNotFound
	}

	return lines, nil
}

// A refusal names only the line that tripped it, because the claim stops at the
// first one. describeFailure turns that into the whole picture, so a caller can
// correct its request in one round trip rather than one line per attempt.
func (store *ReservationStore) Claim(
	ctx context.Context,
	orderID string,
	lines []application.Line,
) error {
	err := store.claim(ctx, orderID, lines)
	if err == nil {
		return nil
	}

	return store.describeFailure(ctx, err, lines)
}

// claim is the transaction itself.
//
// The caller passes canonical lines, which are sorted by SKU. That ordering is
// load-bearing: it fixes the sequence stock rows are locked in, so two requests
// covering the same products can never hold each other's next row.
func (store *ReservationStore) claim(
	ctx context.Context,
	orderID string,
	lines []application.Line,
) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin claim transaction: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	for _, line := range lines {
		if err := claimLine(ctx, transaction, orderID, line); err != nil {
			return err
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit claim transaction: %w", err)
	}

	return nil
}

func claimLine(
	ctx context.Context,
	transaction *sql.Tx,
	orderID string,
	line application.Line,
) error {
	if err := claimReservation(ctx, transaction, orderID, line); err != nil {
		return err
	}

	return takeStock(ctx, transaction, line)
}

func claimReservation(
	ctx context.Context,
	transaction *sql.Tx,
	orderID string,
	line application.Line,
) error {
	result, err := transaction.ExecContext(
		ctx,
		`INSERT INTO reservations (order_id, product_sku, quantity)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (order_id, product_sku) DO NOTHING`,
		orderID,
		line.ProductSKU,
		line.Quantity,
	)
	// The foreign key to stock_items is what rejects a product this service has
	// never heard of, so no separate existence query is needed.
	if isUnknownProduct(err) {
		return fmt.Errorf("%w: %s", application.ErrUnknownProductSKU, line.ProductSKU)
	}
	if err != nil {
		return fmt.Errorf("claim %s for order %s: %w", line.ProductSKU, orderID, err)
	}

	claimed, err := rowsAffected(result, line.ProductSKU)
	if err != nil {
		return err
	}

	// Nothing inserted means the row was already there, so another request
	// carrying this order id got here first.
	if claimed == 0 {
		return application.ErrReservationAlreadyClaimed
	}

	return nil
}

// takeStock is the statement the whole service is built around.
//
// The availability check and the write are one statement, so there is no window
// between deciding and acting. Under READ COMMITTED, a transaction that blocks
// here on a row another transaction holds re-evaluates this WHERE clause against
// the committed version once the lock is released — so the loser sees the
// winner's decrement and matches zero rows.
//
// That is why there is no SELECT ... FOR UPDATE and no SERIALIZABLE: both exist
// to close a read-then-decide gap that this statement does not have.
func takeStock(ctx context.Context, transaction *sql.Tx, line application.Line) error {
	result, err := transaction.ExecContext(
		ctx,
		`UPDATE stock_items
		    SET reserved = reserved + $2,
		        updated_at = now()
		  WHERE product_sku = $1
		    AND on_hand - reserved >= $2`,
		line.ProductSKU,
		line.Quantity,
	)
	if err != nil {
		return fmt.Errorf("reserve stock for %s: %w", line.ProductSKU, err)
	}

	reserved, err := rowsAffected(result, line.ProductSKU)
	if err != nil {
		return err
	}

	// claimReservation has already proved the product exists, so zero rows can
	// only mean there is not enough of it.
	if reserved == 0 {
		return fmt.Errorf("%w: %s", application.ErrInsufficientStock, line.ProductSKU)
	}

	return nil
}

func (store *ReservationStore) describeFailure(
	ctx context.Context,
	claimErr error,
	lines []application.Line,
) error {
	describable := errors.Is(claimErr, application.ErrUnknownProductSKU) ||
		errors.Is(claimErr, application.ErrInsufficientStock)
	if describable == false {
		return claimErr
	}

	available, err := store.availability(ctx, lines)
	if err != nil {
		store.logger.Warn().
			Ctx(ctx).
			Err(err).
			Msg("could not describe a refused claim")

		return claimErr
	}

	// Canonical lines arrive sorted, but sorting here too means the promise that
	// these lists are ordered does not rest on a caller keeping its side of a
	// contract.
	unknown := make([]string, 0)
	for _, line := range lines {
		if _, stocked := available[line.ProductSKU]; stocked == false {
			unknown = append(unknown, line.ProductSKU)
		}
	}

	// A product that does not exist is not a product that is short, so it
	// answers first: NOT_FOUND is the more specific verdict.
	if len(unknown) > 0 {
		slices.Sort(unknown)

		return &application.UnknownProductSKUError{ProductSKUs: unknown}
	}

	shortfalls := make([]application.Shortfall, 0)
	for _, line := range lines {
		if free := available[line.ProductSKU]; free < line.Quantity {
			shortfalls = append(shortfalls, application.Shortfall{
				ProductSKU: line.ProductSKU,
				Requested:  line.Quantity,
				Available:  free,
			})
		}
	}

	// Nothing is short any more, so the warehouse was restocked between the
	// refusal and this query. An empty list would report insufficient stock and
	// name nothing responsible for it, which is worse than saying less.
	if len(shortfalls) == 0 {
		return claimErr
	}

	slices.SortFunc(shortfalls, func(first, second application.Shortfall) int {
		return strings.Compare(first.ProductSKU, second.ProductSKU)
	})

	return &application.InsufficientStockError{Shortfalls: shortfalls}
}

// availability reports what is free for each requested product. A SKU missing
// from the result is one this warehouse does not stock at all.
func (store *ReservationStore) availability(
	ctx context.Context,
	lines []application.Line,
) (map[string]int32, error) {
	productSKUs := make([]string, 0, len(lines))
	for _, line := range lines {
		productSKUs = append(productSKUs, line.ProductSKU)
	}

	rows, err := store.db.QueryContext(
		ctx,
		`SELECT product_sku, on_hand - reserved
		   FROM stock_items
		  WHERE product_sku = ANY($1::text[])`,
		productSKUs,
	)
	if err != nil {
		return nil, fmt.Errorf("query availability: %w", err)
	}
	defer rows.Close()

	available := make(map[string]int32, len(productSKUs))
	for rows.Next() {
		var (
			productSKU string
			free       int32
		)
		if err := rows.Scan(&productSKU, &free); err != nil {
			return nil, fmt.Errorf("scan availability: %w", err)
		}

		available[productSKU] = free
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate availability: %w", err)
	}

	return available, nil
}

func rowsAffected(result sql.Result, productSKU string) (int64, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count affected rows for %s: %w", productSKU, err)
	}

	return affected, nil
}

func isUnknownProduct(err error) bool {
	var postgresError *pgconn.PgError

	return errors.As(err, &postgresError) && postgresError.Code == foreignKeyViolation
}
