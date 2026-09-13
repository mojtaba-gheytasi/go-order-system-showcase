package amqpapi

import (
	"fmt"
	"net/mail"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	orderv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/api/gen/order/v1"
)

// An unknown currency is refused rather than formatted as an empty string.
var supportedCurrencies = map[string]bool{"EUR": true, "USD": true}

// decodeOrderAccepted turns a message body into this service's own command, or
// explains why it never can be.
//
// Every failure here is permanent. A body that will not decode will not decode on the
// fourth attempt either, so retrying wastes the budget and delays the alert — the
// caller turns these into OutcomeInvalid and the message is parked.
//
// The validation is not defensive noise. proto3 has no `required`: every field is
// optional on the wire and absent ones decode to a zero value. So this function *is*
// the required-field half of the contract. Without it the service would cheerfully
// email the empty string about an order worth zero.
//
// It also converts to an application type rather than passing the generated struct
// inward. order-service owns that struct; a consumer that let it reach its use cases
// would have its internal model versioned by another team's release.
//
// The envelope — content type and message type — is checked generically by the
// consumer before this runs, so what arrives here is bytes that claim to be an
// OrderAccepted.
func decodeOrderAccepted(
	messageID string,
	body []byte,
) (application.OrderAcceptedCommand, error) {
	var event orderv1.OrderAccepted
	if err := proto.Unmarshal(body, &event); err != nil {
		return application.OrderAcceptedCommand{}, fmt.Errorf(
			"%w: unmarshal: %w",
			application.ErrInvalidEvent,
			err,
		)
	}

	command, err := commandFrom(&event)
	if err != nil {
		return application.OrderAcceptedCommand{}, err
	}

	// The envelope and the payload must agree about which event this is. They are set
	// from the same value by the publisher, so a mismatch means something rewrote one
	// of them in transit, and nothing downstream should trust either.
	if messageID != "" && messageID != command.EventID {
		return application.OrderAcceptedCommand{}, fmt.Errorf(
			"%w: message id %q does not match event id %q",
			application.ErrInvalidEvent,
			messageID,
			command.EventID,
		)
	}

	return command, nil
}

func commandFrom(event *orderv1.OrderAccepted) (application.OrderAcceptedCommand, error) {
	if strings.TrimSpace(event.GetEventId()) == "" {
		return application.OrderAcceptedCommand{}, missing("event_id")
	}

	if strings.TrimSpace(event.GetOrderId()) == "" {
		return application.OrderAcceptedCommand{}, missing("order_id")
	}

	email := strings.TrimSpace(event.GetCustomerEmail())
	if _, err := mail.ParseAddress(email); err != nil {
		return application.OrderAcceptedCommand{}, fmt.Errorf(
			"%w: customer_email is not a valid address: %w",
			application.ErrInvalidEvent,
			err,
		)
	}

	total := event.GetTotalAmount()
	if total == nil {
		return application.OrderAcceptedCommand{}, missing("total_amount")
	}

	if supportedCurrencies[total.GetCurrency()] == false {
		return application.OrderAcceptedCommand{}, fmt.Errorf(
			"%w: unsupported currency %q",
			application.ErrInvalidEvent,
			total.GetCurrency(),
		)
	}

	if total.GetAmountInMinorUnits() < 0 {
		return application.OrderAcceptedCommand{}, fmt.Errorf(
			"%w: total_amount is negative",
			application.ErrInvalidEvent,
		)
	}

	if event.GetOccurredAt() == nil {
		return application.OrderAcceptedCommand{}, missing("occurred_at")
	}

	occurredAt := event.GetOccurredAt().AsTime()
	if occurredAt.IsZero() {
		return application.OrderAcceptedCommand{}, missing("occurred_at")
	}

	// An order with no lines is not something to send a confirmation about. The
	// lines are validated even though the email this service currently writes does
	// not list them: the event says they are there, and accepting a contradiction
	// now only moves the failure to whoever adds them to the template.
	if len(event.GetLines()) == 0 {
		return application.OrderAcceptedCommand{}, missing("lines")
	}

	for index, line := range event.GetLines() {
		if err := validateLine(index, line); err != nil {
			return application.OrderAcceptedCommand{}, err
		}
	}

	return application.OrderAcceptedCommand{
		EventID:                 event.GetEventId(),
		OrderID:                 event.GetOrderId(),
		CustomerEmail:           email,
		TotalAmountInMinorUnits: total.GetAmountInMinorUnits(),
		Currency:                total.GetCurrency(),
		OccurredAt:              occurredAt,
	}, nil
}

func validateLine(index int, line *orderv1.OrderLine) error {
	if line == nil {
		return fmt.Errorf("%w: line %d is empty", application.ErrInvalidEvent, index)
	}

	if strings.TrimSpace(line.GetProductSku()) == "" {
		return fmt.Errorf("%w: line %d has no product_sku", application.ErrInvalidEvent, index)
	}

	if line.GetQuantity() <= 0 {
		return fmt.Errorf(
			"%w: line %d has quantity %d, want greater than zero",
			application.ErrInvalidEvent,
			index,
			line.GetQuantity(),
		)
	}

	unitPrice := line.GetUnitPrice()
	if unitPrice == nil {
		return fmt.Errorf("%w: line %d has no unit_price", application.ErrInvalidEvent, index)
	}

	if supportedCurrencies[unitPrice.GetCurrency()] == false {
		return fmt.Errorf(
			"%w: line %d has unsupported currency %q",
			application.ErrInvalidEvent,
			index,
			unitPrice.GetCurrency(),
		)
	}

	if unitPrice.GetAmountInMinorUnits() < 0 {
		return fmt.Errorf("%w: line %d has a negative unit_price", application.ErrInvalidEvent, index)
	}

	return nil
}

func missing(field string) error {
	return fmt.Errorf("%w: %s is required", application.ErrInvalidEvent, field)
}
