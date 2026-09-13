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
	events    OrderEventPublisher
	clock     Clock
	newID     IDGenerator
	logger    zerolog.Logger
}

func NewCreateOrder(
	orders OrderRepository,
	catalog ProductCatalog,
	inventory InventoryReserver,
	events OrderEventPublisher,
	clock Clock,
	newID IDGenerator,
	logger zerolog.Logger,
) *CreateOrder {
	return &CreateOrder{
		orders:    orders,
		catalog:   catalog,
		inventory: inventory,
		events:    events,
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
		// A repeat of an order already refused. The stored verdict is all there
		// is: which lines were short was inventory's answer at the time, and
		// replaying it now would state old stock levels as current fact.
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
	err := useCase.reserveInventory(ctx, reservationRequestFor(order))
	if err != nil {
		if errors.Is(err, ErrInsufficientStock) {
			// The refusal is carried through rather than replaced by the
			// sentinel: whatever inventory said about which lines blocked the
			// order is the only thing a customer can act on.
			return useCase.reject(ctx, order, created, err)
		}

		return CreateOrderResult{}, fmt.Errorf("reserve inventory: %w", err)
	}

	if err := order.Accept(useCase.clock()); err != nil {
		return CreateOrderResult{}, fmt.Errorf("accept order: %w", err)
	}

	if err := useCase.orders.Update(ctx, order, domain.StatusPending); err != nil {
		if errors.Is(err, ErrOrderStateConflict) {
			return useCase.resultAfterConflict(ctx, order.ID(), created)
		}

		return CreateOrderResult{}, fmt.Errorf("persist accepted order: %w", err)
	}

	useCase.publish(ctx, order)

	return CreateOrderResult{Order: order, Created: created}, nil
}

// reject records that the order cannot be filled and hands back the refusal that
// caused it. The order is stored as rejected either way; cause is returned
// unchanged so that the detail inventory supplied survives being persisted.
func (useCase *CreateOrder) reject(
	ctx context.Context,
	order *domain.Order,
	created bool,
	cause error,
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

	return CreateOrderResult{}, cause
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
) error {
	var lastErr error

	for attempt := 0; attempt < inventoryReservationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := useCase.inventory.Reserve(ctx, request)
		if err == nil {
			return nil
		}

		lastErr = err
		if errors.Is(err, ErrInventoryUnavailable) == false || attempt == inventoryReservationAttempts-1 {
			return err
		}

		delay := inventoryRetryInitialDelay * time.Duration(attempt+1)
		if err := waitForRetry(ctx, delay); err != nil {
			return err
		}
	}

	return lastErr
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

	// Every unpriced product is collected before giving up, so a customer with
	// several bad skus learns about all of them at once instead of one per
	// attempt.
	missing := make([]string, 0)
	for _, commandItem := range command.Items {
		productSKU := strings.TrimSpace(commandItem.ProductSKU)
		if _, found := prices[productSKU]; found == false {
			missing = append(missing, productSKU)
		}
	}

	if len(missing) > 0 {
		return nil, &ProductNotFoundError{ProductSKUs: missing}
	}

	items := make([]domain.OrderItem, 0, len(command.Items))
	for _, commandItem := range command.Items {
		productSKU := strings.TrimSpace(commandItem.ProductSKU)

		item, err := domain.NewOrderItem(productSKU, commandItem.Quantity, prices[productSKU])
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

// publish announces the acceptance, and swallows any failure.
//
// The order is already durably accepted at this point. Turning a publication
// failure into an API error would tell the client its order failed when it did
// not, inviting a retry that cannot help — the retry finds the order accepted and
// publishes nothing, because only the request that stores the acceptance
// publishes. There is no outbox, so a lost event stays lost and this log line is
// the only record of it.
//
// The snapshot is taken here rather than inside the adapter so that the event's
// identity and timestamp are decided once, by the layer that owns the fact.
func (useCase *CreateOrder) publish(ctx context.Context, order *domain.Order) {
	event := newOrderAcceptedEvent(useCase.newID(), order)

	if err := useCase.events.PublishOrderAccepted(ctx, event); err != nil {
		useCase.logger.Warn().
			Err(err).
			Str("order_id", string(order.ID())).
			Str("event_id", event.EventID).
			Msg("order accepted but its event was not published")
	}
}
