// Package wiring is the order bounded context's composition root: the single
// place allowed to know every concrete adapter sitting behind the application
// layer's ports. nothing under domain/, application/, or adapter/ may import this package.
package wiring

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/inbound/httpgin"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/catalogstub"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/inventorystub"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/notificationlog"
	orderpostgres "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
)

type Dependencies struct {
	DB     *sql.DB
	Logger zerolog.Logger
	Clock  func() time.Time
	NewID  func() string
}

// Module is what the order context contributes to the process.
type Module struct {
	HTTPHandler http.Handler
}

func New(dependencies Dependencies) (*Module, error) {

	now := application.Clock(func() time.Time {
		return dependencies.Clock().Truncate(time.Microsecond)
	})
	newID := application.IDGenerator(dependencies.NewID)
	inventory := inventorystub.NewReserver(newID, dependencies.Logger)

	createOrder := application.NewCreateOrder(
		orderpostgres.NewOrderRepository(dependencies.DB),
		catalogstub.New(),
		inventory,
		notificationlog.NewNotifier(dependencies.Logger),
		now,
		newID,
		dependencies.Logger,
	)

	return &Module{
		HTTPHandler: httpgin.NewRouter(httpgin.RouterConfig{
			Logger:      dependencies.Logger,
			CreateOrder: createOrder,
		}),
	}, nil
}
