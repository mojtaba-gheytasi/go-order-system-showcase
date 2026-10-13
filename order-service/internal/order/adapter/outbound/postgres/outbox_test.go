//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/correlation"
)

const (
	longLease = 30 * time.Second

	// Long enough for the claim statement to commit, short enough that a test
	// can wait it out.
	shortLease = 50 * time.Millisecond
)

func TestEventOutboxClaimsAStoredEventExactlyAsItWasWritten(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	stored := seedEvents(t, db, 1)[0]

	entry, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)

	assert.Equal(t, stored.EventID, entry.EventID)
	assert.Equal(t, "request-1", entry.CorrelationID)
	assert.Equal(t, 0, entry.Attempts)
	assert.NotEmpty(t, entry.ClaimToken)
	assertEventsEqual(t, stored, entry.Event)
}

func TestEventOutboxClaimsOnlyDuePendingEvents(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	events := seedEvents(t, db, 3)

	published, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	require.NoError(t, outbox.MarkPublished(ctx, published.EventID))

	setAside, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	require.NoError(t, outbox.MarkNeedsAttention(ctx, setAside, true, "no queue bound"))

	leased, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)

	_, err = outbox.ClaimNext(ctx, longLease)
	require.ErrorIs(t, err, application.ErrOutboxEmpty, "published, set aside and leased rows are all skipped")

	assert.ElementsMatch(
		t,
		[]string{events[0].EventID, events[1].EventID, events[2].EventID},
		[]string{published.EventID, setAside.EventID, leased.EventID},
	)
	assert.Equal(t, "published", outboxRow(t, db, published.EventID).status)
	assert.Equal(t, "needs_attention", outboxRow(t, db, setAside.EventID).status)
	assert.Equal(t, 1, outboxRow(t, db, setAside.EventID).attempts)
}

func TestEventOutboxNeverHandsTheSameEventToTwoConcurrentClaimers(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	const events = 8
	seedEvents(t, db, events)

	claimed := make([]string, events)
	var start, finished sync.WaitGroup
	start.Add(1)
	for index := range events {
		finished.Add(1)
		go func() {
			defer finished.Done()
			start.Wait()

			entry, err := outbox.ClaimNext(ctx, longLease)
			if err == nil {
				claimed[index] = entry.EventID
			}
		}()
	}
	start.Done()
	finished.Wait()

	unique := make(map[string]bool)
	for _, eventID := range claimed {
		if eventID != "" {
			assert.False(t, unique[eventID], "event %s was claimed twice", eventID)
			unique[eventID] = true
		}
	}
	assert.Len(t, unique, events, "SKIP LOCKED moves each claimer on to a free row")
}

// Release is for an outage, which is not the event's fault, so it must not use
// up budget. ScheduleRetry is for a failure of this event, so it must.
func TestEventOutboxCountsOnlyRetriesAgainstTheBudget(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	seedEvents(t, db, 1)

	entry, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	require.NoError(t, outbox.Release(ctx, entry, 0))

	entry, err = outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	assert.Equal(t, 0, entry.Attempts, "a release is free")

	require.NoError(t, outbox.ScheduleRetry(ctx, entry, 0, "no queue bound"))

	entry, err = outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	assert.Equal(t, 1, entry.Attempts, "a retry costs one attempt")
	assert.Equal(t, "no queue bound", outboxRow(t, db, entry.EventID).lastError)

	require.NoError(t, outbox.ScheduleRetry(ctx, entry, time.Hour, "no queue bound"))
	_, err = outbox.ClaimNext(ctx, longLease)
	require.ErrorIs(t, err, application.ErrOutboxEmpty, "the retry is not due for an hour")
}

// A worker that dies mid-publish leaves its claim behind. Once the lease runs out
// the event is claimed again, unchanged.
func TestEventOutboxReclaimsAnEventWhoseLeaseRanOut(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	stored := seedEvents(t, db, 1)[0]

	abandoned, err := outbox.ClaimNext(ctx, shortLease)
	require.NoError(t, err)
	time.Sleep(2 * shortLease)

	reclaimed, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)

	assert.Equal(t, abandoned.EventID, reclaimed.EventID)
	assert.NotEqual(t, abandoned.ClaimToken, reclaimed.ClaimToken)
	assertEventsEqual(t, stored, reclaimed.Event)
}

// A stalls, its lease runs out, B claims. Nothing A writes afterwards may touch
// B's claim: a retry would change when the row is due, and a set-aside would
// stop B's attempt from counting.
func TestEventOutboxRefusesWritesFromAClaimThatWasTakenOver(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	seedEvents(t, db, 1)

	stale, err := outbox.ClaimNext(ctx, shortLease)
	require.NoError(t, err)
	time.Sleep(2 * shortLease)
	current, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)

	require.ErrorIs(t, outbox.Release(ctx, stale, 0), application.ErrOutboxClaimLost)
	require.ErrorIs(t, outbox.ScheduleRetry(ctx, stale, 0, "late"), application.ErrOutboxClaimLost)
	require.ErrorIs(t, outbox.MarkNeedsAttention(ctx, stale, true, "late"), application.ErrOutboxClaimLost)

	row := outboxRow(t, db, current.EventID)
	assert.Equal(t, "pending", row.status)
	assert.Equal(t, 0, row.attempts)
	assert.False(t, row.due, "B's lease still holds")
}

// Unlike the writes above, a late success is recorded: RabbitMQ routed the event
// whoever held the lease, and nothing B does afterwards can make it due again.
func TestEventOutboxKeepsALateSuccessFromATakenOverClaim(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	seedEvents(t, db, 1)

	stale, err := outbox.ClaimNext(ctx, shortLease)
	require.NoError(t, err)
	time.Sleep(2 * shortLease)
	current, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)

	require.NoError(t, outbox.MarkPublished(ctx, stale.EventID))
	require.ErrorIs(t, outbox.ScheduleRetry(ctx, current, 0, "late"), application.ErrOutboxClaimLost)

	assert.Equal(t, "published", outboxRow(t, db, current.EventID).status)
	_, err = outbox.ClaimNext(ctx, longLease)
	require.ErrorIs(t, err, application.ErrOutboxEmpty)
}

func TestEventOutboxReportsAnUnreadableEventSoItCanBeSetAside(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	stored := seedEvents(t, db, 1)[0]

	_, err := db.ExecContext(ctx, `UPDATE outbox_events SET payload = '"not an event"'`)
	require.NoError(t, err)

	entry, err := outbox.ClaimNext(ctx, longLease)
	require.ErrorIs(t, err, application.ErrEventUnpublishable)
	assert.Equal(t, stored.EventID, entry.EventID, "enough to set the row aside")

	require.NoError(t, outbox.MarkNeedsAttention(ctx, entry, false, err.Error()))
	row := outboxRow(t, db, stored.EventID)
	assert.Equal(t, "needs_attention", row.status)
	assert.Equal(t, 0, row.attempts, "it never reached the broker")
}

// The same statement `make order-outbox-redrive` runs.
func TestRedrivingAnEventGivesItAFreshBudget(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(t, ctx)
	outbox := postgres.NewEventOutbox(db)
	stored := seedEvents(t, db, 1)[0]

	entry, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	require.NoError(t, outbox.ScheduleRetry(ctx, entry, 0, "no queue bound"))
	entry, err = outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	require.NoError(t, outbox.MarkNeedsAttention(ctx, entry, true, "no queue bound"))

	_, err = db.ExecContext(ctx, `
		UPDATE outbox_events
		   SET status = 'pending', attempts = 0, next_attempt_at = now(), needs_attention_at = NULL
		 WHERE status = 'needs_attention'`)
	require.NoError(t, err)

	redriven, err := outbox.ClaimNext(ctx, longLease)
	require.NoError(t, err)
	assert.Equal(t, stored.EventID, redriven.EventID)
	assert.Equal(t, 0, redriven.Attempts)
	assert.Equal(t, "no queue bound", outboxRow(t, db, stored.EventID).lastError, "kept for history")
}

// seedEvents accepts count orders through the repository, so each event is
// stored exactly as order creation stores it.
func seedEvents(t *testing.T, db *sql.DB, count int) []application.OrderAcceptedEvent {
	t.Helper()

	repository := postgres.NewOrderRepository(db)
	ctx := correlation.WithRequestID(context.Background(), "request-1")

	events := make([]application.OrderAcceptedEvent, 0, count)
	for index := range count {
		order := newTestOrder(
			t,
			domain.OrderID(fmt.Sprintf("018f0f38-5a52-7a01-8000-%012d", 100+index)),
			fmt.Sprintf("order-%d", index),
		)
		require.NoError(t, repository.Create(ctx, order))
		require.NoError(t, order.Accept(order.CreatedAt().Add(time.Minute)))

		event := acceptedEventFor(order, fmt.Sprintf("018f0f38-5a52-7a01-8000-%012d", 900+index))
		require.NoError(t, repository.UpdateAndRecordEvent(ctx, order, domain.StatusPending, event))

		events = append(events, event)
	}

	return events
}

func assertEventsEqual(t *testing.T, expected, actual application.OrderAcceptedEvent) {
	t.Helper()

	assert.Equal(t, expected.EventID, actual.EventID)
	assert.Equal(t, expected.OrderID, actual.OrderID)
	assert.True(t, expected.OccurredAt.Equal(actual.OccurredAt))
	assert.Equal(t, expected.CustomerEmail, actual.CustomerEmail)
	assert.Equal(t, expected.Total, actual.Total)
	assert.Equal(t, expected.Lines, actual.Lines)
}

type storedOutboxRow struct {
	status    string
	attempts  int
	lastError string
	due       bool
}

func outboxRow(t *testing.T, db *sql.DB, eventID string) storedOutboxRow {
	t.Helper()

	var row storedOutboxRow
	var lastError sql.NullString
	err := db.QueryRow(`
		SELECT status, attempts, last_error, next_attempt_at <= now()
		  FROM outbox_events
		 WHERE event_id = $1`, eventID,
	).Scan(&row.status, &row.attempts, &lastError, &row.due)
	require.NoError(t, err)
	row.lastError = lastError.String

	return row
}
