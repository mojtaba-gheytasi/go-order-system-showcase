package application

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests call publishNext directly, one event per call, so the delays it
// chooses can be checked without waiting them out. Run gets its own tests at the
// end.

const testPollInterval = time.Second

var errNotRouted = errors.New("no queue was bound to receive the message")

func TestWorkerMarksARoutedEventPublishedAndMovesOnAtOnce(t *testing.T) {
	worker := newTestWorker(claimed(testEntry(0)))

	pause := worker.useCase.publishNext(context.Background())

	assert.Zero(t, pause)
	assert.Equal(t, []string{"event-1"}, worker.outbox.published)
	require.Len(t, worker.publisher.calls, 1)
	assert.Equal(t, "request-1", worker.publisher.calls[0].correlationID)
	assert.Equal(t, "event-1", worker.publisher.calls[0].event.EventID)
}

// An unreachable broker is not this event's fault, so it must not use up the
// event's budget. The worker pauses instead, longer each time, until an attempt
// reaches the broker again.
func TestWorkerWaitsOutAnUnreachableBrokerWithoutSpendingBudget(t *testing.T) {
	worker := newTestWorker(
		claimed(testEntry(0)), claimed(testEntry(0)), claimed(testEntry(0)),
		claimed(testEntry(0)), claimed(testEntry(0)),
	)
	worker.publisher.results = []error{
		ErrPublisherUnavailable, ErrPublisherUnavailable, ErrPublisherUnavailable,
		nil,
		ErrPublisherUnavailable,
	}

	pauses := make([]time.Duration, 0, 5)
	for range 5 {
		pauses = append(pauses, worker.useCase.publishNext(context.Background()))
	}

	assert.Equal(t, []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second,
		0,
		5 * time.Second, // reset by the attempt that reached the broker
	}, pauses)
	assert.Empty(t, worker.outbox.retries, "an outage spends no budget")
	assert.Empty(t, worker.outbox.setAside)
	require.Len(t, worker.outbox.released, 4)
	assert.Equal(t, 20*time.Second, worker.outbox.released[2].delay, "the row waits as long as the worker")
}

func TestWorkerRetriesAFailedPublicationWithExponentialBackoff(t *testing.T) {
	tests := map[string]error{
		"not routed":      errNotRouted,
		"outcome unknown": ErrPublicationOutcomeUnknown,
	}

	for name, publishErr := range tests {
		t.Run(name, func(t *testing.T) {
			worker := newTestWorker(claimed(testEntry(2)))
			worker.publisher.results = []error{publishErr}

			pause := worker.useCase.publishNext(context.Background())

			assert.Equal(t, testPollInterval, pause)
			require.Len(t, worker.outbox.retries, 1)
			retry := worker.outbox.retries[0]
			assert.Equal(t, 20*time.Second, retry.delay, "the third attempt waits 5s doubled twice")
			assert.Contains(t, retry.reason, publishErr.Error())
			assert.Empty(t, worker.outbox.published)
		})
	}
}

func TestWorkerSetsAnEventAsideWhenItsBudgetIsUsedUp(t *testing.T) {
	worker := newTestWorker(claimed(testEntry(MaxPublishAttempts - 1)))
	worker.publisher.results = []error{errNotRouted}

	worker.useCase.publishNext(context.Background())

	assert.Empty(t, worker.outbox.retries)
	require.Len(t, worker.outbox.setAside, 1)
	assert.True(t, worker.outbox.setAside[0].countAttempt)
	assert.Contains(t, worker.logs.String(), `"level":"error"`)
	assert.Contains(t, worker.logs.String(), "event needs attention")
	assert.Contains(t, worker.logs.String(), `"attempts":10`)
}

// Retrying cannot fix an event that cannot be read or encoded, so it needs a
// person straight away, and no attempt is counted against it.
func TestWorkerSetsAnUnpublishableEventAsideAtOnce(t *testing.T) {
	t.Run("unreadable in storage", func(t *testing.T) {
		worker := newTestWorker(claimResult{entry: testEntry(0), err: ErrEventUnpublishable})

		pause := worker.useCase.publishNext(context.Background())

		assert.Zero(t, pause)
		assert.Empty(t, worker.publisher.calls, "nothing is published")
		require.Len(t, worker.outbox.setAside, 1)
		assert.False(t, worker.outbox.setAside[0].countAttempt)
	})

	t.Run("cannot be encoded", func(t *testing.T) {
		worker := newTestWorker(claimed(testEntry(0)))
		worker.publisher.results = []error{ErrEventUnpublishable}

		worker.useCase.publishNext(context.Background())

		require.Len(t, worker.outbox.setAside, 1)
		assert.False(t, worker.outbox.setAside[0].countAttempt)
		assert.Empty(t, worker.outbox.retries)
	})
}

func TestWorkerPollsAgainWhenNothingIsDueOrTheClaimFails(t *testing.T) {
	tests := map[string]error{
		"nothing due":   ErrOutboxEmpty,
		"claim failure": errors.New("connection refused"),
	}

	for name, claimErr := range tests {
		t.Run(name, func(t *testing.T) {
			worker := newTestWorker(claimResult{err: claimErr})

			pause := worker.useCase.publishNext(context.Background())

			assert.Equal(t, testPollInterval, pause)
			assert.Empty(t, worker.publisher.calls)
		})
	}
}

// A failed write only means the event is tried again once its lease runs out:
// a possible duplicate, never a loss, so it must not stop the worker.
func TestWorkerLogsAFailedOutcomeWriteAndCarriesOn(t *testing.T) {
	worker := newTestWorker(claimed(testEntry(0)))
	worker.outbox.writeErr = errors.New("database unavailable")

	pause := worker.useCase.publishNext(context.Background())

	assert.Zero(t, pause)
	assert.Contains(t, worker.logs.String(), "mark outbox event published")
	assert.Contains(t, worker.logs.String(), "database unavailable")
}

func TestWorkerLeavesARowAnotherWorkerHasTakenOver(t *testing.T) {
	worker := newTestWorker(claimed(testEntry(0)))
	worker.publisher.results = []error{errNotRouted}
	worker.outbox.writeErr = ErrOutboxClaimLost

	worker.useCase.publishNext(context.Background())

	assert.Contains(t, worker.logs.String(), "claimed by another worker")
	assert.NotContains(t, worker.logs.String(), "schedule outbox event retry")
}

func TestRetryDelayDoublesUpToTheCapAndJittersWithinItsUpperHalf(t *testing.T) {
	tests := []struct {
		attempt int
		delay   time.Duration
	}{
		{attempt: 1, delay: 5 * time.Second},
		{attempt: 2, delay: 10 * time.Second},
		{attempt: 6, delay: 160 * time.Second},
		{attempt: 7, delay: 5 * time.Minute},
		{attempt: 50, delay: 5 * time.Minute},
	}

	for _, test := range tests {
		longest := retryDelay(test.attempt, func(n int64) int64 { return n - 1 })
		shortest := retryDelay(test.attempt, func(int64) int64 { return 0 })

		assert.Equal(t, test.delay, longest, "attempt %d", test.attempt)
		assert.Equal(t, test.delay/2, shortest, "attempt %d", test.attempt)
	}
}

// Cleanup only runs when there is nothing to publish, so it never delays an
// event, and at most once an hour, so an idle worker does not run it every poll.
func TestWorkerDeletesOldPublishedEventsWhenIdleAtMostHourly(t *testing.T) {
	worker := newTestWorker()

	worker.useCase.publishNext(context.Background())
	require.Len(t, worker.outbox.deleteCalls, 1, "the first idle poll cleans up")
	assert.Equal(t, 30*24*time.Hour, worker.outbox.deleteCalls[0].olderThan, "published events are kept for a month")

	worker.now = worker.now.Add(59 * time.Minute)
	worker.useCase.publishNext(context.Background())
	assert.Len(t, worker.outbox.deleteCalls, 1, "not again within the hour")

	worker.now = worker.now.Add(time.Minute)
	worker.useCase.publishNext(context.Background())
	assert.Len(t, worker.outbox.deleteCalls, 2, "again once the hour is up")
}

func TestWorkerDoesNotCleanUpWhileThereIsWorkToDo(t *testing.T) {
	worker := newTestWorker(claimed(testEntry(0)))

	worker.useCase.publishNext(context.Background())

	assert.Empty(t, worker.outbox.deleteCalls)
}

// A backlog is deleted one batch per idle poll, so an event that arrives in the
// middle is claimed after at most one batch, not after the whole backlog.
func TestWorkerDeletesABacklogOneBatchAtATime(t *testing.T) {
	worker := newTestWorker()
	worker.outbox.deleteResults = []deleteResult{
		{deleted: outboxCleanupBatchSize},
		{deleted: outboxCleanupBatchSize},
		{deleted: 7},
	}

	worker.useCase.publishNext(context.Background())
	assert.Len(t, worker.outbox.deleteCalls, 1, "one batch, then back to claiming")

	worker.outbox.claims = []claimResult{claimed(testEntry(0))}
	worker.useCase.publishNext(context.Background())
	assert.Equal(t, []string{"event-1"}, worker.outbox.published, "the new event goes first")
	assert.Len(t, worker.outbox.deleteCalls, 1)

	worker.useCase.publishNext(context.Background())
	worker.useCase.publishNext(context.Background())
	assert.Len(t, worker.outbox.deleteCalls, 3, "the round carries on without waiting an hour")

	worker.useCase.publishNext(context.Background())
	assert.Len(t, worker.outbox.deleteCalls, 3, "a batch that was not full ends the round")
}

func TestWorkerKeepsPublishingWhenCleanupFails(t *testing.T) {
	worker := newTestWorker()
	worker.outbox.deleteResults = []deleteResult{{err: errors.New("database unavailable")}}

	pause := worker.useCase.publishNext(context.Background())

	assert.Equal(t, testPollInterval, pause)
	assert.Contains(t, worker.logs.String(), "delete old published outbox events")
}

// Shutdown must not cut a claimed event in half: the publication finishes and
// its outcome is written, even though the context is cancelled meanwhile.
func TestRunFinishesTheClaimedEventWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := newTestWorker(claimed(testEntry(0)))
	worker.publisher.duringPublish = cancel

	done := make(chan struct{})
	go func() {
		worker.useCase.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	require.Len(t, worker.publisher.calls, 1)
	assert.NoError(t, worker.publisher.calls[0].ctxErr, "the publication is detached from shutdown")
	assert.Equal(t, []string{"event-1"}, worker.outbox.published)
	assert.Equal(t, []error{nil}, worker.outbox.writeCtxErrs, "the outcome write is detached too")
	assert.Equal(t, 1, worker.outbox.claimCalls, "nothing new is claimed after cancellation")
}

func TestRunReturnsWhileWaitingForWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := newTestWorker()
	worker.outbox.onEmpty = cancel

	done := make(chan struct{})
	go func() {
		worker.useCase.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

type testWorker struct {
	useCase   *PublishOrderEvents
	outbox    *fakeOutbox
	publisher *fakePublisher
	logs      *bytes.Buffer

	// now is what the worker's clock reads; a test moves it forward.
	now time.Time
}

func newTestWorker(claims ...claimResult) *testWorker {
	worker := &testWorker{
		outbox:    &fakeOutbox{claims: claims},
		publisher: &fakePublisher{},
		logs:      &bytes.Buffer{},
		now:       time.Date(2026, time.October, 13, 9, 0, 0, 0, time.UTC),
	}

	worker.useCase = NewPublishOrderEvents(
		worker.outbox,
		worker.publisher,
		30*time.Second,
		testPollInterval,
		// The longest delay, so expected values read as plain doublings.
		func(n int64) int64 { return n - 1 },
		func() time.Time { return worker.now },
		zerolog.New(worker.logs),
	)

	return worker
}

func testEntry(attempts int) OutboxEntry {
	return OutboxEntry{
		EventID:       "event-1",
		Event:         OrderAcceptedEvent{EventID: "event-1", OrderID: "order-1"},
		CorrelationID: "request-1",
		Attempts:      attempts,
		ClaimToken:    "token-1",
	}
}

type claimResult struct {
	entry OutboxEntry
	err   error
}

func claimed(entry OutboxEntry) claimResult { return claimResult{entry: entry} }

type releaseCall struct {
	entry OutboxEntry
	delay time.Duration
}

type retryCall struct {
	entry  OutboxEntry
	delay  time.Duration
	reason string
}

type attentionCall struct {
	entry        OutboxEntry
	countAttempt bool
	reason       string
}

type fakeOutbox struct {
	claims     []claimResult
	claimCalls int
	onEmpty    func()

	writeErr     error
	writeCtxErrs []error

	published []string
	released  []releaseCall
	retries   []retryCall
	setAside  []attentionCall

	// deleteResults are returned by DeletePublished in turn; once used up, it
	// deletes nothing.
	deleteResults []deleteResult
	deleteCalls   []deleteCall
}

type deleteResult struct {
	deleted int
	err     error
}

type deleteCall struct {
	olderThan time.Duration
	limit     int
}

func (outbox *fakeOutbox) DeletePublished(_ context.Context, olderThan time.Duration, limit int) (int, error) {
	outbox.deleteCalls = append(outbox.deleteCalls, deleteCall{olderThan: olderThan, limit: limit})
	if len(outbox.deleteResults) == 0 {
		return 0, nil
	}

	next := outbox.deleteResults[0]
	outbox.deleteResults = outbox.deleteResults[1:]

	return next.deleted, next.err
}

func (outbox *fakeOutbox) ClaimNext(context.Context, time.Duration) (OutboxEntry, error) {
	outbox.claimCalls++
	if len(outbox.claims) == 0 {
		if outbox.onEmpty != nil {
			outbox.onEmpty()
		}

		return OutboxEntry{}, ErrOutboxEmpty
	}

	next := outbox.claims[0]
	outbox.claims = outbox.claims[1:]

	return next.entry, next.err
}

func (outbox *fakeOutbox) MarkPublished(ctx context.Context, eventID string) error {
	outbox.writeCtxErrs = append(outbox.writeCtxErrs, ctx.Err())
	outbox.published = append(outbox.published, eventID)

	return outbox.writeErr
}

func (outbox *fakeOutbox) Release(ctx context.Context, entry OutboxEntry, delay time.Duration) error {
	outbox.writeCtxErrs = append(outbox.writeCtxErrs, ctx.Err())
	outbox.released = append(outbox.released, releaseCall{entry: entry, delay: delay})

	return outbox.writeErr
}

func (outbox *fakeOutbox) ScheduleRetry(
	ctx context.Context,
	entry OutboxEntry,
	delay time.Duration,
	reason string,
) error {
	outbox.writeCtxErrs = append(outbox.writeCtxErrs, ctx.Err())
	outbox.retries = append(outbox.retries, retryCall{entry: entry, delay: delay, reason: reason})

	return outbox.writeErr
}

func (outbox *fakeOutbox) MarkNeedsAttention(
	ctx context.Context,
	entry OutboxEntry,
	countAttempt bool,
	reason string,
) error {
	outbox.writeCtxErrs = append(outbox.writeCtxErrs, ctx.Err())
	outbox.setAside = append(outbox.setAside, attentionCall{
		entry:        entry,
		countAttempt: countAttempt,
		reason:       reason,
	})

	return outbox.writeErr
}

type publishCall struct {
	event         OrderAcceptedEvent
	correlationID string
	ctxErr        error
}

type fakePublisher struct {
	results       []error
	calls         []publishCall
	duringPublish func()
}

func (publisher *fakePublisher) PublishOrderAccepted(
	ctx context.Context,
	event OrderAcceptedEvent,
	correlationID string,
) error {
	if publisher.duringPublish != nil {
		publisher.duringPublish()
	}

	publisher.calls = append(publisher.calls, publishCall{
		event:         event,
		correlationID: correlationID,
		ctxErr:        ctx.Err(),
	})

	index := len(publisher.calls) - 1
	if index < len(publisher.results) {
		return publisher.results[index]
	}

	return nil
}
