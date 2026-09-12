package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

const (
	insertOrderQuery = `
		INSERT INTO orders (
			id,
			customer_id,
			customer_email,
			status,
			idempotency_key,
			total_amount_in_cents,
			currency,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	insertOrderItemQuery = `
		INSERT INTO order_items (
			order_id,
			line_number,
			product_sku,
			quantity,
			unit_amount_in_cents
		) VALUES ($1, $2, $3, $4, $5)`

	updateOrderStateQuery = `
		UPDATE orders
		SET
			status = $1,
			updated_at = $2
		WHERE id = $3 AND status = $4`

	findOrderByIDQuery = `
		SELECT
			id,
			customer_id,
			customer_email,
			status,
			idempotency_key,
			total_amount_in_cents,
			currency,
			created_at,
			updated_at
		FROM orders
		WHERE id = $1`

	findOrderByIdempotencyKeyQuery = `
		SELECT
			id,
			customer_id,
			customer_email,
			status,
			idempotency_key,
			total_amount_in_cents,
			currency,
			created_at,
			updated_at
		FROM orders
		WHERE idempotency_key = $1`

	findOrderItemsQuery = `
		SELECT
			order_id,
			line_number,
			product_sku,
			quantity,
			unit_amount_in_cents
		FROM order_items
		WHERE order_id = $1
		ORDER BY line_number`

	idempotencyConstraint = "orders_idempotency_key_uidx"
)

type OrderRepository struct {
	db *sql.DB
}

var _ application.OrderRepository = (*OrderRepository)(nil)

func NewOrderRepository(db *sql.DB) *OrderRepository {
	return &OrderRepository{db: db}
}

func (repository *OrderRepository) Create(ctx context.Context, order *domain.Order) error {
	storedOrder, storedOrderItems, err := rowsFromDomain(order)
	if err != nil {
		return fmt.Errorf("map order for creation: %w", err)
	}

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create order transaction: %w", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	_, err = transaction.ExecContext(
		ctx,
		insertOrderQuery,
		storedOrder.id,
		storedOrder.customerID,
		storedOrder.customerEmail,
		storedOrder.status,
		storedOrder.idempotencyKey,
		storedOrder.totalAmountInCents,
		storedOrder.currency,
		storedOrder.createdAt,
		storedOrder.updatedAt,
	)
	if err != nil {
		if isDuplicateIdempotencyKey(err) {
			return application.ErrDuplicateIdempotencyKey
		}

		return fmt.Errorf("insert order: %w", err)
	}

	for _, orderItem := range storedOrderItems {
		_, err = transaction.ExecContext(
			ctx,
			insertOrderItemQuery,
			orderItem.orderID,
			orderItem.lineNumber,
			orderItem.productSKU,
			orderItem.quantity,
			orderItem.unitAmountInCents,
		)
		if err != nil {
			return fmt.Errorf("insert order item at line %d: %w", orderItem.lineNumber, err)
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit create order transaction: %w", err)
	}

	return nil
}

func (repository *OrderRepository) Update(
	ctx context.Context,
	order *domain.Order,
	expectedStatus domain.Status,
) error {
	storedOrder, _, err := rowsFromDomain(order)
	if err != nil {
		return fmt.Errorf("map order for update: %w", err)
	}

	result, err := repository.db.ExecContext(
		ctx,
		updateOrderStateQuery,
		storedOrder.status,
		storedOrder.updatedAt,
		storedOrder.id,
		string(expectedStatus),
	)
	if err != nil {
		return fmt.Errorf("update order state: %w", err)
	}

	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated order count: %w", err)
	}
	if updated == 0 {
		return application.ErrOrderStateConflict
	}

	return nil
}

func (repository *OrderRepository) FindByID(
	ctx context.Context,
	id domain.OrderID,
) (*domain.Order, error) {
	parsedID, err := uuid.Parse(string(id))
	if err != nil {
		return nil, fmt.Errorf("parse order id: %w", err)
	}

	storedOrder, err := scanOrder(repository.db.QueryRowContext(ctx, findOrderByIDQuery, parsedID))
	if err != nil {
		return nil, err
	}

	return repository.loadOrder(ctx, storedOrder)
}

func (repository *OrderRepository) FindByIdempotencyKey(
	ctx context.Context,
	key string,
) (*domain.Order, error) {
	storedOrder, err := scanOrder(
		repository.db.QueryRowContext(ctx, findOrderByIdempotencyKeyQuery, key),
	)
	if err != nil {
		return nil, err
	}

	return repository.loadOrder(ctx, storedOrder)
}

func (repository *OrderRepository) loadOrder(
	ctx context.Context,
	storedOrder orderRow,
) (*domain.Order, error) {
	rows, err := repository.db.QueryContext(ctx, findOrderItemsQuery, storedOrder.id)
	if err != nil {
		return nil, fmt.Errorf("query order items: %w", err)
	}
	defer rows.Close()

	storedOrderItems := make([]orderItemRow, 0)
	for rows.Next() {
		var storedOrderItem orderItemRow
		if err := rows.Scan(
			&storedOrderItem.orderID,
			&storedOrderItem.lineNumber,
			&storedOrderItem.productSKU,
			&storedOrderItem.quantity,
			&storedOrderItem.unitAmountInCents,
		); err != nil {
			return nil, fmt.Errorf("scan order item: %w", err)
		}

		storedOrderItems = append(storedOrderItems, storedOrderItem)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order items: %w", err)
	}

	return domainFromRows(storedOrder, storedOrderItems)
}

func scanOrder(row *sql.Row) (orderRow, error) {
	var storedOrder orderRow
	err := row.Scan(
		&storedOrder.id,
		&storedOrder.customerID,
		&storedOrder.customerEmail,
		&storedOrder.status,
		&storedOrder.idempotencyKey,
		&storedOrder.totalAmountInCents,
		&storedOrder.currency,
		&storedOrder.createdAt,
		&storedOrder.updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return orderRow{}, application.ErrOrderNotFound
	}
	if err != nil {
		return orderRow{}, fmt.Errorf("scan order: %w", err)
	}

	return storedOrder, nil
}

func isDuplicateIdempotencyKey(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) &&
		postgresError.Code == "23505" &&
		postgresError.ConstraintName == idempotencyConstraint
}
