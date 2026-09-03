// Package inventorystub is a TEMPORARY in-process stand-in for the inventory
// service. It always succeeds and remembers one reservation per order, so it
// proves the create-order flow and its idempotency contract without a second
// service running.
//
// It is replaced by adapter/outbound/inventorygrpc once inventory-service
// exists. If a test needs a reservation to fail, it should substitute its own
// application.InventoryReserver rather than adding failure knobs to this stub.
package inventorystub

import (
	"context"
	"sync"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type Reserver struct {
	newID        application.IDGenerator
	logger       zerolog.Logger
	mutex        sync.Mutex
	reservations map[domain.OrderID]domain.ReservationID
}

var _ application.InventoryReserver = (*Reserver)(nil)

func NewReserver(newID application.IDGenerator, logger zerolog.Logger) *Reserver {
	return &Reserver{
		newID:        newID,
		logger:       logger,
		reservations: make(map[domain.OrderID]domain.ReservationID),
	}
}

func (reserver *Reserver) Reserve(
	ctx context.Context,
	request application.ReservationRequest,
) (domain.ReservationID, error) {
	reserver.mutex.Lock()
	defer reserver.mutex.Unlock()

	if reservationID, found := reserver.reservations[request.OrderID]; found {
		reserver.logger.Info().
			Str("reservation_id", string(reservationID)).
			Str("order_id", string(request.OrderID)).
			Msg("existing stock reservation returned by the inventory stub")

		return reservationID, nil
	}

	reservationID := domain.ReservationID(reserver.newID())
	reserver.reservations[request.OrderID] = reservationID

	reserver.logger.Info().
		Str("reservation_id", string(reservationID)).
		Str("order_id", string(request.OrderID)).
		Int("lines", len(request.Lines)).
		Msg("stock reserved by the inventory stub")

	return reservationID, nil
}
