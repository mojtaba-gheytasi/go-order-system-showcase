// Package wiring is the order bounded context's composition root: the single
// place allowed to know every concrete adapter sitting behind the application
// layer's ports. nothing under domain/, application/, or adapter/ may import this package.
package wiring

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/inbound/httpgin"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/catalogstub"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/inventorygrpc"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/ordereventamqp"
	orderpostgres "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
)

type Dependencies struct {
	DB               *sql.DB
	Logger           zerolog.Logger
	Clock            func() time.Time
	NewID            func() string
	InventoryConn    *grpc.ClientConn
	InventoryTimeout time.Duration
	EventPublisher   ordereventamqp.MessagePublisher
}

type Module struct {
	HTTPHandler http.Handler
}

func New(dependencies Dependencies) (*Module, error) {

	now := application.Clock(func() time.Time {
		return dependencies.Clock().Truncate(time.Microsecond)
	})
	newID := application.IDGenerator(dependencies.NewID)
	inventory := inventorygrpc.NewReserver(
		dependencies.InventoryConn,
		dependencies.InventoryTimeout,
	)

	createOrder := application.NewCreateOrder(
		orderpostgres.NewOrderRepository(dependencies.DB),
		catalogstub.New(),
		inventory,
		ordereventamqp.NewPublisher(dependencies.EventPublisher),
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
