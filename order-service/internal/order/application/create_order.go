package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

const (
	inventoryReservationAttempts = 3
	inventoryRetryInitialDelay   = 100 * time.Millisecond
)

type CreateOrderItem struct {
	ProductSKU string
	Quantity   int
}

type CreateOrderCommand struct {
	IdempotencyKey string
	CustomerID     string
	CustomerEmail  string
	Items          []CreateOrderItem
}

type CreateOrderResult struct {
	Order   *domain.Order
	Created bool
}

type CreateOrder struct {
	orders    OrderRepository
	catalog   ProductCatalog
	inventory InventoryReserver
	notifier  OrderNotifier
	clock     Clock
	newID     IDGenerator
	logger    zerolog.Logger
}

func NewCreateOrder(
	orders OrderRepository,
	catalog ProductCatalog,
	inventory InventoryReserver,
	notifier OrderNotifier,
	clock Clock,
	newID IDGenerator,
	logger zerolog.Logger,
) *CreateOrder {
	return &CreateOrder{
		orders:    orders,
		catalog:   catalog,
		inventory: inventory,
		notifier:  notifier,
		clock:     clock,
		newID:     newID,
		logger:    logger,
	}
}

func (useCase *CreateOrder) Execute(
	ctx context.Context,
	command CreateOrderCommand,
) (CreateOrderResult, error) {
	order, err := useCase.orders.FindByIdempotencyKey(ctx, command.IdempotencyKey)
	if err == nil {
		return useCase.processPersisted(ctx, order, false)
	}
	if errors.Is(err, ErrOrderNotFound) == false {
		return CreateOrderResult{}, fmt.Errorf("find order by idempotency key: %w", err)
	}

	order, err = useCase.buildOrder(ctx, command)
	if err != nil {
		return CreateOrderResult{}, err
	}

	// Persist the pending order before crossing the inventory boundary. This
	// gives inventory a stable OrderID and leaves visible, recoverable state if
	// the network call fails.
	if err := useCase.orders.Create(ctx, order); err != nil {
		if errors.Is(err, ErrDuplicateIdempotencyKey) {
			winner, findErr := useCase.orders.FindByIdempotencyKey(ctx, command.IdempotencyKey)
			if findErr != nil {
				return CreateOrderResult{}, fmt.Errorf("load concurrent order: %w", findErr)
			}

			return useCase.processPersisted(ctx, winner, false)
		}

		return CreateOrderResult{}, fmt.Errorf("create pending order: %w", err)
	}

	return useCase.processPersisted(ctx, order, true)
}

func (useCase *CreateOrder) processPersisted(
	ctx context.Context,
	order *domain.Order,
	created bool,
) (CreateOrderResult, error) {
	switch order.Status() {
	case domain.StatusAccepted, domain.StatusShipped, domain.StatusCancelled:
		return CreateOrderResult{Order: order, Created: created}, nil
	case domain.StatusRejected:
		return CreateOrderResult{}, ErrInsufficientStock
	case domain.StatusPending:
		return useCase.processPending(ctx, order, created)
	default:
		return CreateOrderResult{}, fmt.Errorf("process order %s: unsupported status %q", order.ID(), order.Status())
	}
}

func (useCase *CreateOrder) processPending(
	ctx context.Context,
	order *domain.Order,
	created bool,
) (CreateOrderResult, error) {
	reservationID, err := useCase.reserveInventory(ctx, reservationRequestFor(order))
	if err != nil {
		if errors.Is(err, ErrInsufficientStock) {
			return useCase.reject(ctx, order, created)
		}

		return CreateOrderResult{}, fmt.Errorf("reserve inventory: %w", err)
	}

	if err := order.Accept(reservationID, useCase.clock()); err != nil {
		return CreateOrderResult{}, fmt.Errorf("accept order: %w", err)
	}

	if err := useCase.orders.Update(ctx, order, domain.StatusPending); err != nil {
		if errors.Is(err, ErrOrderStateConflict) {
			return useCase.resultAfterConflict(ctx, order.ID(), created)
		}

		return CreateOrderResult{}, fmt.Errorf("persist accepted order: %w", err)
	}

	useCase.notify(ctx, order)

	return CreateOrderResult{Order: order, Created: created}, nil
}

func (useCase *CreateOrder) reject(
	ctx context.Context,
	order *domain.Order,
	created bool,
) (CreateOrderResult, error) {
	if err := order.Reject(useCase.clock()); err != nil {
		return CreateOrderResult{}, fmt.Errorf("reject order: %w", err)
	}

	if err := useCase.orders.Update(ctx, order, domain.StatusPending); err != nil {
		if errors.Is(err, ErrOrderStateConflict) {
			return useCase.resultAfterConflict(ctx, order.ID(), created)
		}

		return CreateOrderResult{}, fmt.Errorf("persist rejected order: %w", err)
	}

	return CreateOrderResult{}, ErrInsufficientStock
}

func (useCase *CreateOrder) resultAfterConflict(
	ctx context.Context,
	orderID domain.OrderID,
	created bool,
) (CreateOrderResult, error) {
	order, err := useCase.orders.FindByID(ctx, orderID)
	if err != nil {
		return CreateOrderResult{}, fmt.Errorf("reload order after state conflict: %w", err)
	}

	switch order.Status() {
	case domain.StatusAccepted, domain.StatusShipped, domain.StatusCancelled:
		return CreateOrderResult{Order: order, Created: created}, nil
	case domain.StatusRejected:
		return CreateOrderResult{}, ErrInsufficientStock
	case domain.StatusPending:
		return CreateOrderResult{}, fmt.Errorf("reload order after state conflict: %w", ErrOrderStateConflict)
	default:
		return CreateOrderResult{}, fmt.Errorf(
			"reload order after state conflict: unsupported status %q",
			order.Status(),
		)
	}
}

func (useCase *CreateOrder) reserveInventory(
	ctx context.Context,
	request ReservationRequest,
) (domain.ReservationID, error) {
	var lastErr error

	for attempt := 0; attempt < inventoryReservationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		reservationID, err := useCase.inventory.Reserve(ctx, request)
		if err == nil {
			return reservationID, nil
		}

		lastErr = err
		if errors.Is(err, ErrInventoryUnavailable) == false || attempt == inventoryReservationAttempts-1 {
			return "", err
		}

		delay := inventoryRetryInitialDelay * time.Duration(attempt+1)
		if err := waitForRetry(ctx, delay); err != nil {
			return "", err
		}
	}

	return "", lastErr
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (useCase *CreateOrder) buildOrder(
	ctx context.Context,
	command CreateOrderCommand,
) (*domain.Order, error) {
	prices, err := useCase.catalog.Prices(ctx, productSKUs(command.Items))
	if err != nil {
		return nil, fmt.Errorf("resolve product prices: %w", err)
	}

	items := make([]domain.OrderItem, 0, len(command.Items))
	for _, commandItem := range command.Items {
		productSKU := strings.TrimSpace(commandItem.ProductSKU)
		price, found := prices[productSKU]
		if found == false {
			return nil, fmt.Errorf("%w: product %q", ErrProductNotFound, productSKU)
		}

		item, err := domain.NewOrderItem(productSKU, commandItem.Quantity, price)
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	order, err := domain.NewOrder(
		domain.OrderID(useCase.newID()),
		domain.CustomerID(command.CustomerID),
		command.CustomerEmail,
		command.IdempotencyKey,
		items,
		useCase.clock(),
	)
	if err != nil {
		return nil, err
	}

	return order, nil
}

func reservationRequestFor(order *domain.Order) ReservationRequest {
	items := order.OrderItems()
	lines := make([]ReservationLine, 0, len(items))
	for _, item := range items {
		lines = append(lines, ReservationLine{
			ProductSKU: item.ProductSKU(),
			Quantity:   item.Quantity(),
		})
	}

	return ReservationRequest{
		OrderID: order.ID(),
		Lines:   lines,
	}
}

func productSKUs(items []CreateOrderItem) []string {
	productSKUs := make([]string, 0, len(items))
	for _, item := range items {
		productSKUs = append(productSKUs, strings.TrimSpace(item.ProductSKU))
	}

	return productSKUs
}

func (useCase *CreateOrder) notify(ctx context.Context, order *domain.Order) {
	if err := useCase.notifier.NotifyOrderCreated(ctx, order); err != nil {
		useCase.logger.Warn().
			Err(err).
			Str("order_id", string(order.ID())).
			Msg("failed to notify accepted order")
	}
}
