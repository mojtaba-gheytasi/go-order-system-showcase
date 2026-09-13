package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// MaxSendAttempts is how many times the email provider is called for one effect
// before the message is parked for a human.
const MaxSendAttempts = 3

// The generated protobuf type deliberately does not reach this layer: the wire format
// belongs to order-service.
type OrderAcceptedCommand struct {
	EventID                 string
	OrderID                 string
	CustomerEmail           string
	TotalAmountInMinorUnits int64
	Currency                string
	OccurredAt              time.Time
}

type HandleOrderAccepted struct {
	store  SentNotificationStore
	sender EmailSender
	lease  time.Duration
	logger zerolog.Logger
}

func NewHandleOrderAccepted(
	store SentNotificationStore,
	sender EmailSender,
	lease time.Duration,
	logger zerolog.Logger,
) *HandleOrderAccepted {
	return &HandleOrderAccepted{
		store:  store,
		sender: sender,
		lease:  lease,
		logger: logger,
	}
}

// Execute sends the confirmation for one accepted order, at most once.
func (useCase *HandleOrderAccepted) Execute(
	ctx context.Context,
	command OrderAcceptedCommand,
) Result {
	key := EffectKey{
		SubjectID: command.OrderID,
		Kind:      KindOrderConfirmation,
		Channel:   ChannelEmail,
	}

	state, claim, err := useCase.store.Claim(ctx, key, command.EventID, useCase.lease)
	if err != nil {
		return Result{
			Outcome: OutcomeStorageFailure,
			Err:     fmt.Errorf("claim notification: %w", err),
		}
	}

	switch state {
	case ClaimAlreadySent:
		return Result{Outcome: OutcomeAlreadySent}
	case ClaimHeldByAnother:
		return Result{Outcome: OutcomeClaimBusy}
	case ClaimGranted:
	default:
		return Result{
			Outcome: OutcomeStorageFailure,
			Err:     fmt.Errorf("claim notification: unsupported claim state %d", state),
		}
	}

	// The budget is checked before the attempt is recorded, so an exhausted effect
	// never reaches the provider. This is what makes a failure to park safe: the
	// message comes back, re-claims, lands here again, and is parked again — it
	// cannot fall through to a fourth send.
	if claim.Attempts >= MaxSendAttempts {
		return Result{Outcome: OutcomeExhausted, Attempts: claim.Attempts}
	}

	attempts, err := useCase.store.BeginAttempt(ctx, key, claim.Token)
	if err != nil {
		if errors.Is(err, ErrClaimLost) {
			// The lease expired and somebody else owns this effect now. Stop
			// without touching the record.
			return Result{Outcome: OutcomeClaimBusy, Err: err}
		}

		return Result{
			Outcome: OutcomeStorageFailure,
			Err:     fmt.Errorf("begin attempt: %w", err),
		}
	}

	sendErr := useCase.sender.Send(ctx, Email{
		To:                      command.CustomerEmail,
		OrderID:                 command.OrderID,
		TotalAmountInMinorUnits: command.TotalAmountInMinorUnits,
		Currency:                command.Currency,
		IdempotencyKey:          ProviderIdempotencyKey(key),
	})
	if sendErr != nil {
		// Released so a retry need not wait out the lease.
		if err := useCase.store.MarkFailed(ctx, key, claim.Token); err != nil {
			useCase.logger.Warn().
				Err(err).
				Str("order_id", command.OrderID).
				Msg("could not release the notification claim after a failed send")
		}

		return Result{
			Outcome:  OutcomeTransientFailure,
			Attempts: attempts,
			Err:      fmt.Errorf("send email: %w", sendErr),
		}
	}

	if err := useCase.store.MarkSent(ctx, key, claim.Token); err != nil {
		// The email is gone and the delivery goes back, so the next attempt may send
		// a second one. This is the window, not a closed case.
		return Result{
			Outcome:  OutcomeStorageFailure,
			Attempts: attempts,
			Err:      fmt.Errorf("record sent notification after sending: %w", err),
		}
	}

	return Result{Outcome: OutcomeSent, Attempts: attempts}
}

// From the effect and never the event id: two events describing one effect must present
// the same key. Hashed so it does not carry the subject's id into a third party's logs.
func ProviderIdempotencyKey(key EffectKey) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s",
		key.SubjectID,
		key.Kind,
		key.Channel,
	)))

	return hex.EncodeToString(sum[:])
}
