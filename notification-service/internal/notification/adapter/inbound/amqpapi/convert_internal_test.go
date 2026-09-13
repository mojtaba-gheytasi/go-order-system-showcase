package amqpapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	orderv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/gen/order/v1"
)

func TestDecodeAcceptsAValidEvent(t *testing.T) {
	command, err := decodeOrderAccepted(messageAndBody(t, validEvent()))
	require.NoError(t, err)

	assert.Equal(t, "event-1", command.EventID)
	assert.Equal(t, "order-1", command.OrderID)
	assert.Equal(t, "buyer@example.test", command.CustomerEmail)
	assert.Equal(t, int64(3000), command.TotalAmountInMinorUnits)
	assert.Equal(t, "EUR", command.Currency)
}

// proto3 has no `required`, so every one of these decodes successfully into a zero
// value and would otherwise be emailed to nobody about nothing. This table is the
// required-field half of the contract.
func TestDecodeRejectsEventsThatCannotBeActedOn(t *testing.T) {
	for name, mutate := range map[string]func(*orderv1.OrderAccepted){
		"no event id":        func(event *orderv1.OrderAccepted) { event.EventId = "" },
		"no order id":        func(event *orderv1.OrderAccepted) { event.OrderId = "" },
		"no email":           func(event *orderv1.OrderAccepted) { event.CustomerEmail = "" },
		"malformed email":    func(event *orderv1.OrderAccepted) { event.CustomerEmail = "not-an-address" },
		"no total":           func(event *orderv1.OrderAccepted) { event.TotalAmount = nil },
		"negative total":     func(event *orderv1.OrderAccepted) { event.TotalAmount.AmountInMinorUnits = -1 },
		"unknown currency":   func(event *orderv1.OrderAccepted) { event.TotalAmount.Currency = "XYZ" },
		"no currency":        func(event *orderv1.OrderAccepted) { event.TotalAmount.Currency = "" },
		"no timestamp":       func(event *orderv1.OrderAccepted) { event.OccurredAt = nil },
		"no lines":           func(event *orderv1.OrderAccepted) { event.Lines = nil },
		"line without sku":   func(event *orderv1.OrderAccepted) { event.Lines[0].ProductSku = "" },
		"line without price": func(event *orderv1.OrderAccepted) { event.Lines[0].UnitPrice = nil },
		"zero quantity":      func(event *orderv1.OrderAccepted) { event.Lines[0].Quantity = 0 },
		"negative quantity":  func(event *orderv1.OrderAccepted) { event.Lines[0].Quantity = -2 },
		"line currency":      func(event *orderv1.OrderAccepted) { event.Lines[0].UnitPrice.Currency = "XYZ" },
	} {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(event)

			_, err := decodeOrderAccepted(messageAndBody(t, event))

			// Permanent, so the consumer parks it immediately instead of spending
			// three attempts discovering the same thing.
			require.ErrorIs(t, err, application.ErrInvalidEvent)
		})
	}
}

func TestDecodeRejectsABodyThatIsNotProtobuf(t *testing.T) {
	_, err := decodeOrderAccepted("event-1", []byte("this is not protobuf at all"))

	require.ErrorIs(t, err, application.ErrInvalidEvent)
}

// The publisher sets both from one value, so a disagreement means something rewrote
// one of them and neither can be trusted.
func TestDecodeRejectsAMessageIdThatContradictsThePayload(t *testing.T) {
	_, body := messageAndBody(t, validEvent())

	_, err := decodeOrderAccepted("a-different-event", body)

	require.ErrorIs(t, err, application.ErrInvalidEvent)
}

// An old timestamp is normal: a message that waited in the retry queue has one.
// Refusing on clock difference alone would park perfectly good events.
func TestDecodeAcceptsAnOldTimestamp(t *testing.T) {
	event := validEvent()
	event.OccurredAt = timestamppb.New(time.Now().Add(-72 * time.Hour))

	_, err := decodeOrderAccepted(messageAndBody(t, event))

	require.NoError(t, err)
}

func validEvent() *orderv1.OrderAccepted {
	return &orderv1.OrderAccepted{
		EventId:       "event-1",
		OrderId:       "order-1",
		OccurredAt:    timestamppb.New(time.Date(2026, time.September, 13, 10, 0, 0, 0, time.UTC)),
		CustomerEmail: "buyer@example.test",
		TotalAmount:   &orderv1.Money{AmountInMinorUnits: 3000, Currency: "EUR"},
		Lines: []*orderv1.OrderLine{{
			ProductSku: "SKU-A",
			Quantity:   2,
			UnitPrice:  &orderv1.Money{AmountInMinorUnits: 1500, Currency: "EUR"},
		}},
	}
}

func messageAndBody(t *testing.T, event *orderv1.OrderAccepted) (string, []byte) {
	t.Helper()

	body, err := proto.Marshal(event)
	require.NoError(t, err)

	return event.GetEventId(), body
}
