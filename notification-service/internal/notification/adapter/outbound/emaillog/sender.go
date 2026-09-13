// Package emaillog stands in for an email provider by logging what it would send.
//
// A real provider would demonstrate nothing this system is trying to show: the
// interesting behaviour is the retry budget, the claim and the parked queue, all of
// which need a sender that can be made to fail. It still receives the provider
// idempotency key, so the seam a real integration slots into is real.
package emaillog

import (
	"context"
	"strings"

	"github.com/rs/zerolog"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
)

type Sender struct {
	logger zerolog.Logger
}

var _ application.EmailSender = (*Sender)(nil)

func NewSender(logger zerolog.Logger) *Sender {
	return &Sender{logger: logger}
}

func (sender *Sender) Send(_ context.Context, email application.Email) error {
	sender.logger.Info().
		Str("recipient", redactEmail(email.To)).
		Str("order_id", email.OrderID).
		Int64("total_amount_in_minor_units", email.TotalAmountInMinorUnits).
		Str("currency", email.Currency).
		Str("provider_idempotency_key", email.IdempotencyKey).
		Msg("order confirmation email sent")

	return nil
}

// redactEmail keeps enough to recognise an address in a log line and not enough to be a
// mailing list. Logs are copied into places with different access rules than a database.
func redactEmail(address string) string {
	local, domain, found := strings.Cut(address, "@")
	if found == false || local == "" {
		return "[redacted]"
	}

	return local[:1] + "***@" + domain
}
