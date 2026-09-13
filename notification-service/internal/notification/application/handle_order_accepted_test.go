package application_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
)

const testLease = time.Minute

func TestSendsTheConfirmationAndRecordsIt(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimGranted}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeSent, result.Outcome)
	assert.Equal(t, 1, result.Attempts)
	assert.Equal(t, 1, sender.calls)
	assert.Equal(t, 1, store.markSentCalls)
	assert.Equal(t, 0, store.markFailedCalls)

	require.Len(t, sender.sent, 1)
	assert.Equal(t, "buyer@example.test", sender.sent[0].To)
	assert.Equal(t, int64(3000), sender.sent[0].TotalAmountInMinorUnits)
}

// The ordinary redelivery. RabbitMQ delivers at least once, so this is the expected
// path rather than an exceptional one, and it must not send a second email.
func TestDoesNotSendWhenTheEffectAlreadyHappened(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimAlreadySent}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeAlreadySent, result.Outcome)
	assert.Equal(t, 0, sender.calls, "an already-sent effect must not reach the provider")
	assert.Equal(t, 0, store.beginAttemptCalls, "and must not spend a send attempt")
}

// The case a plain "have I sent this?" check cannot express. Another worker holds the
// claim, so this delivery has to come back later — and crucially it must not be
// counted as a send attempt, because no send was attempted.
func TestDefersWhenAnotherWorkerHoldsTheClaim(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimHeldByAnother}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeClaimBusy, result.Outcome)
	assert.Equal(t, 0, sender.calls)
	assert.Equal(t, 0, store.beginAttemptCalls, "a busy claim must not spend the budget")
}

// The budget guard. Reached when three provider calls have already been made, and the
// sender must not be called a fourth time — this is what makes a failure to park safe,
// because the message comes back, lands here, and is parked again instead of sending.
func TestStopsWithoutSendingOnceTheBudgetIsSpent(t *testing.T) {
	for _, attempts := range []int{application.MaxSendAttempts, application.MaxSendAttempts + 1} {
		store := &fakeStore{claimState: application.ClaimGranted, attempts: attempts}
		sender := &fakeSender{}

		result := newUseCase(store, sender).Execute(context.Background(), testCommand())

		assert.Equal(t, application.OutcomeExhausted, result.Outcome)
		assert.Equal(t, 0, sender.calls, "an exhausted effect must never reach the provider")
		assert.Equal(t, 0, store.beginAttemptCalls)
	}
}

func TestAttemptsBelowTheBudgetStillSend(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimGranted, attempts: application.MaxSendAttempts - 1}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeSent, result.Outcome)
	assert.Equal(t, 1, sender.calls)
}

// A stale worker discovers it lost the effect when it tries to spend the budget. It
// must stop rather than send: the worker that took the claim over may already have
// sent the email.
func TestStopsWhenTheClaimWasLostBeforeSending(t *testing.T) {
	store := &fakeStore{
		claimState:      application.ClaimGranted,
		beginAttemptErr: application.ErrClaimLost,
	}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeClaimBusy, result.Outcome)
	assert.Equal(t, 0, sender.calls, "a fenced-out worker must not send")
}

func TestReleasesTheClaimWhenSendingFails(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimGranted}
	sender := &fakeSender{err: errors.New("provider timeout")}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeTransientFailure, result.Outcome)
	assert.Equal(t, 1, result.Attempts, "a real provider call was made, so it counts")
	// Released rather than left holding the lease, so the retry does not have to wait
	// the lease out before it can try again.
	assert.Equal(t, 1, store.markFailedCalls)
	assert.Equal(t, 0, store.markSentCalls)
}

// The window this design documents rather than closes. The email is gone and the
// record is not written, so the delivery comes back and may send a second one. The
// outcome has to be honest about that: it is a storage failure, not a success.
func TestReportsAStorageFailureWhenTheRecordCannotBeWritten(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimGranted, markSentErr: errors.New("no connection")}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeStorageFailure, result.Outcome)
	assert.Equal(t, 1, sender.calls, "the email did go out")
	require.Error(t, result.Err)
}

func TestReportsAStorageFailureWhenTheClaimCannotBeTaken(t *testing.T) {
	store := &fakeStore{claimErr: errors.New("no connection")}
	sender := &fakeSender{}

	result := newUseCase(store, sender).Execute(context.Background(), testCommand())

	assert.Equal(t, application.OutcomeStorageFailure, result.Outcome)
	assert.Equal(t, 0, sender.calls)
}

// The provider key is derived from the effect, never from the event id. Two events
// describing the same effect must present the same key, or a provider deduplicating
// on it would send both.
func TestTheProviderKeyDependsOnTheEffectAndNotTheEvent(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimGranted}
	sender := &fakeSender{}
	useCase := newUseCase(store, sender)

	first := testCommand()
	first.EventID = "event-1"
	second := testCommand()
	second.EventID = "event-2"

	useCase.Execute(context.Background(), first)
	store.claimState = application.ClaimGranted
	useCase.Execute(context.Background(), second)

	require.Len(t, sender.sent, 2)
	assert.Equal(t, sender.sent[0].IdempotencyKey, sender.sent[1].IdempotencyKey,
		"the same effect under two event ids must present one provider key")
}

func TestTheProviderKeyDiffersBetweenOrders(t *testing.T) {
	first := application.ProviderIdempotencyKey(application.EffectKey{
		SubjectID: "order-1",
		Kind:      application.KindOrderConfirmation,
		Channel:   application.ChannelEmail,
	})
	second := application.ProviderIdempotencyKey(application.EffectKey{
		SubjectID: "order-2",
		Kind:      application.KindOrderConfirmation,
		Channel:   application.ChannelEmail,
	})

	assert.NotEqual(t, first, second)
}

// Kind is part of the identity for a reason: a second notification about the same
// order must not be deduplicated away as the confirmation having already been sent.
func TestTheProviderKeyDiffersBetweenNotificationKinds(t *testing.T) {
	confirmation := application.ProviderIdempotencyKey(application.EffectKey{
		SubjectID: "order-1",
		Kind:      application.KindOrderConfirmation,
		Channel:   application.ChannelEmail,
	})
	other := application.ProviderIdempotencyKey(application.EffectKey{
		SubjectID: "order-1",
		Kind:      "order_shipped",
		Channel:   application.ChannelEmail,
	})

	assert.NotEqual(t, confirmation, other)
}

func TestClaimsTheEffectRatherThanTheEvent(t *testing.T) {
	store := &fakeStore{claimState: application.ClaimGranted}

	newUseCase(store, &fakeSender{}).Execute(context.Background(), testCommand())

	assert.Equal(t, application.EffectKey{
		SubjectID: "order-1",
		Kind:      application.KindOrderConfirmation,
		Channel:   application.ChannelEmail,
	}, store.claimedKey)
	assert.Equal(t, testLease, store.claimedLease)
}

func newUseCase(
	store application.SentNotificationStore,
	sender application.EmailSender,
) *application.HandleOrderAccepted {
	return application.NewHandleOrderAccepted(store, sender, testLease, zerolog.New(io.Discard))
}

func testCommand() application.OrderAcceptedCommand {
	return application.OrderAcceptedCommand{
		EventID:                 "event-1",
		OrderID:                 "order-1",
		CustomerEmail:           "buyer@example.test",
		TotalAmountInMinorUnits: 3000,
		Currency:                "EUR",
		OccurredAt:              time.Date(2026, time.September, 13, 10, 0, 0, 0, time.UTC),
	}
}

type fakeStore struct {
	claimState application.ClaimState
	claimErr   error
	attempts   int

	beginAttemptErr error
	markSentErr     error

	claimedKey   application.EffectKey
	claimedLease time.Duration

	beginAttemptCalls int
	markSentCalls     int
	markFailedCalls   int
}

var _ application.SentNotificationStore = (*fakeStore)(nil)

func (store *fakeStore) Claim(
	_ context.Context,
	key application.EffectKey,
	_ string,
	lease time.Duration,
) (application.ClaimState, application.Claim, error) {
	store.claimedKey = key
	store.claimedLease = lease

	if store.claimErr != nil {
		return 0, application.Claim{}, store.claimErr
	}

	return store.claimState, application.Claim{Token: "token-1", Attempts: store.attempts}, nil
}

func (store *fakeStore) BeginAttempt(
	_ context.Context,
	_ application.EffectKey,
	_ string,
) (int, error) {
	store.beginAttemptCalls++

	if store.beginAttemptErr != nil {
		return 0, store.beginAttemptErr
	}

	return store.attempts + 1, nil
}

func (store *fakeStore) MarkSent(_ context.Context, _ application.EffectKey, _ string) error {
	store.markSentCalls++

	return store.markSentErr
}

func (store *fakeStore) MarkFailed(_ context.Context, _ application.EffectKey, _ string) error {
	store.markFailedCalls++

	return nil
}

type fakeSender struct {
	err   error
	calls int
	sent  []application.Email
}

var _ application.EmailSender = (*fakeSender)(nil)

func (sender *fakeSender) Send(_ context.Context, email application.Email) error {
	sender.calls++
	sender.sent = append(sender.sent, email)

	return sender.err
}
