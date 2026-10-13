package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

const eventTypeOrderAccepted = "order_accepted"

// orderAcceptedPayload is how an OrderAcceptedEvent is stored in the outbox. The
// event and order ids live in their own columns, so they are not repeated here.
//
// It is JSON rather than the protobuf message that will be published: the wire
// format belongs to the AMQP adapter, and JSON can be read with psql.
type orderAcceptedPayload struct {
	OccurredAt         time.Time                  `json:"occurred_at"`
	CustomerEmail      string                     `json:"customer_email"`
	TotalAmountInCents int64                      `json:"total_amount_in_cents"`
	Currency           string                     `json:"currency"`
	Lines              []orderAcceptedPayloadLine `json:"lines"`
}

type orderAcceptedPayloadLine struct {
	ProductSKU        string `json:"product_sku"`
	Quantity          int    `json:"quantity"`
	UnitAmountInCents int64  `json:"unit_amount_in_cents"`
	Currency          string `json:"currency"`
}

func payloadFromEvent(event application.OrderAcceptedEvent) ([]byte, error) {
	lines := make([]orderAcceptedPayloadLine, 0, len(event.Lines))
	for _, line := range event.Lines {
		lines = append(lines, orderAcceptedPayloadLine{
			ProductSKU:        line.ProductSKU,
			Quantity:          line.Quantity,
			UnitAmountInCents: line.UnitPrice.AmountInCents,
			Currency:          line.UnitPrice.Currency,
		})
	}

	return json.Marshal(orderAcceptedPayload{
		OccurredAt:         event.OccurredAt,
		CustomerEmail:      event.CustomerEmail,
		TotalAmountInCents: event.Total.AmountInCents,
		Currency:           event.Total.Currency,
		Lines:              lines,
	})
}

func eventFromPayload(
	eventID string,
	orderID string,
	payload []byte,
) (application.OrderAcceptedEvent, error) {
	var stored orderAcceptedPayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		return application.OrderAcceptedEvent{}, fmt.Errorf("decode payload: %w", err)
	}

	// Valid JSON is not necessarily an event. Publishing an empty one would tell
	// consumers something that never happened.
	if len(stored.Lines) == 0 || stored.CustomerEmail == "" || stored.OccurredAt.IsZero() {
		return application.OrderAcceptedEvent{}, errors.New("payload is not a complete order accepted event")
	}

	lines := make([]application.OrderAcceptedLine, 0, len(stored.Lines))
	for _, line := range stored.Lines {
		lines = append(lines, application.OrderAcceptedLine{
			ProductSKU: line.ProductSKU,
			Quantity:   line.Quantity,
			UnitPrice: domain.Money{
				AmountInCents: line.UnitAmountInCents,
				Currency:      line.Currency,
			},
		})
	}

	return application.OrderAcceptedEvent{
		EventID:       eventID,
		OrderID:       domain.OrderID(orderID),
		OccurredAt:    stored.OccurredAt,
		CustomerEmail: stored.CustomerEmail,
		Total: domain.Money{
			AmountInCents: stored.TotalAmountInCents,
			Currency:      stored.Currency,
		},
		Lines: lines,
	}, nil
}
