package application

import (
	"context"
	"time"
)

type Channel string

const ChannelEmail Channel = "email"

// Part of the effect identity even with one value: without it, an "order shipped" email
// added later would be deduplicated away as the confirmation.
type Kind string

const KindOrderConfirmation Kind = "order_confirmation"

// Not the event id: two events can describe one effect, and what must not happen twice
// is the email.
type EffectKey struct {
	// What the notification is about, not necessarily an order. Kind implies the
	// subject's type, so there is no SubjectType beside it.
	SubjectID string
	Kind      Kind
	Channel   Channel
}

type ClaimState int

const (
	ClaimGranted ClaimState = iota
	ClaimAlreadySent
	ClaimHeldByAnother
)

type Claim struct {
	// Fences the claim: every later write carries it, so a worker whose lease expired
	// cannot overwrite the result of the worker that took over.
	Token string

	// Provider calls made before this claim.
	Attempts int
}

// Shaped around a lease rather than a "have I sent this?" check, because a check
// followed by a send lets two workers both read "not sent" and both send.
type SentNotificationStore interface {
	// Claim must distinguish already-sent from held-by-another. A single "no rows"
	// answer cannot: one means acknowledge the message, the other means try later.
	Claim(
		ctx context.Context,
		key EffectKey,
		eventID string,
		lease time.Duration,
	) (ClaimState, Claim, error)

	// BeginAttempt is separate from Claim so the send budget only moves when a send is
	// about to happen. Counting it in Claim would let deliveries that never reached the
	// provider spend the budget.
	BeginAttempt(ctx context.Context, key EffectKey, token string) (attempts int, err error)

	MarkSent(ctx context.Context, key EffectKey, token string) error

	// MarkFailed releases the claim so a retry need not wait out the lease.
	MarkFailed(ctx context.Context, key EffectKey, token string) error
}

type Email struct {
	To      string
	OrderID string

	TotalAmountInMinorUnits int64
	Currency                string

	// IdempotencyKey is derived from the effect, never the event id, so two events
	// describing one effect present the same key.
	IdempotencyKey string
}

type EmailSender interface {
	Send(ctx context.Context, email Email) error
}
