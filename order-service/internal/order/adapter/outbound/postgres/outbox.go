package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
)

// Every time below comes from the database's now(), never from the caller. Two
// workers on machines whose clocks disagree would otherwise disagree about when a
// lease has run out.
const (
	// One statement, so no transaction stays open while the event is published.
	// SKIP LOCKED lets several workers claim at once without waiting on each other,
	// and moving next_attempt_at forward is the lease that keeps them apart.
	claimOutboxEventQuery = `
		UPDATE outbox_events
		   SET next_attempt_at = now() + $1::interval,
		       claim_token = gen_random_uuid()
		 WHERE event_id = (
		       SELECT event_id
		         FROM outbox_events
		        WHERE status = 'pending' AND next_attempt_at <= now()
		        ORDER BY next_attempt_at
		        LIMIT 1
		          FOR UPDATE SKIP LOCKED)
		RETURNING event_id, order_id, payload, correlation_id, attempts, claim_token`

	// Not fenced by the claim token, unlike the writes below. That RabbitMQ routed
	// the event stays true whoever held the lease, and recording it can only
	// prevent a duplicate. It also applies to a row that already needs attention.
	markOutboxEventPublishedQuery = `
		UPDATE outbox_events
		   SET status = 'published',
		       published_at = now(),
		       last_error = NULL
		 WHERE event_id = $1 AND status <> 'published'`

	releaseOutboxEventQuery = `
		UPDATE outbox_events
		   SET next_attempt_at = now() + $3::interval
		 WHERE event_id = $1 AND claim_token = $2 AND status = 'pending'`

	scheduleOutboxEventRetryQuery = `
		UPDATE outbox_events
		   SET attempts = attempts + 1,
		       next_attempt_at = now() + $3::interval,
		       last_error = $4
		 WHERE event_id = $1 AND claim_token = $2 AND status = 'pending'`

	markOutboxEventNeedsAttentionQuery = `
		UPDATE outbox_events
		   SET status = 'needs_attention',
		       attempts = attempts + $3,
		       needs_attention_at = now(),
		       last_error = $4
		 WHERE event_id = $1 AND claim_token = $2 AND status = 'pending'`
)

type EventOutbox struct {
	db *sql.DB
}

var _ application.EventOutbox = (*EventOutbox)(nil)

func NewEventOutbox(db *sql.DB) *EventOutbox {
	return &EventOutbox{db: db}
}

func (outbox *EventOutbox) ClaimNext(
	ctx context.Context,
	lease time.Duration,
) (application.OutboxEntry, error) {
	var (
		eventID, orderID, correlationID, claimToken string
		payload                                     []byte
		attempts                                    int
	)

	err := outbox.db.QueryRowContext(ctx, claimOutboxEventQuery, intervalFor(lease)).Scan(
		&eventID,
		&orderID,
		&payload,
		&correlationID,
		&attempts,
		&claimToken,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return application.OutboxEntry{}, application.ErrOutboxEmpty
	}
	if err != nil {
		return application.OutboxEntry{}, fmt.Errorf("claim outbox event: %w", err)
	}

	entry := application.OutboxEntry{
		EventID:       eventID,
		CorrelationID: correlationID,
		Attempts:      attempts,
		ClaimToken:    claimToken,
	}

	event, err := eventFromPayload(eventID, orderID, payload)
	if err != nil {
		return entry, fmt.Errorf("%w: outbox event %s: %w", application.ErrEventUnpublishable, eventID, err)
	}
	entry.Event = event

	return entry, nil
}

func (outbox *EventOutbox) MarkPublished(ctx context.Context, eventID string) error {
	if _, err := outbox.db.ExecContext(ctx, markOutboxEventPublishedQuery, eventID); err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}

	return nil
}

func (outbox *EventOutbox) Release(
	ctx context.Context,
	entry application.OutboxEntry,
	delay time.Duration,
) error {
	return outbox.execFenced(
		ctx,
		"release outbox event",
		releaseOutboxEventQuery,
		entry.EventID,
		entry.ClaimToken,
		intervalFor(delay),
	)
}

func (outbox *EventOutbox) ScheduleRetry(
	ctx context.Context,
	entry application.OutboxEntry,
	delay time.Duration,
	reason string,
) error {
	return outbox.execFenced(
		ctx,
		"schedule outbox event retry",
		scheduleOutboxEventRetryQuery,
		entry.EventID,
		entry.ClaimToken,
		intervalFor(delay),
		reason,
	)
}

func (outbox *EventOutbox) MarkNeedsAttention(
	ctx context.Context,
	entry application.OutboxEntry,
	countAttempt bool,
	reason string,
) error {
	increment := 0
	if countAttempt {
		increment = 1
	}

	return outbox.execFenced(
		ctx,
		"mark outbox event as needing attention",
		markOutboxEventNeedsAttentionQuery,
		entry.EventID,
		entry.ClaimToken,
		increment,
		reason,
	)
}

// execFenced runs a write that only the current claim may make. Zero rows means
// the lease ran out and another worker took the row over.
func (outbox *EventOutbox) execFenced(
	ctx context.Context,
	action string,
	query string,
	args ...any,
) error {
	result, err := outbox.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if affected == 0 {
		return application.ErrOutboxClaimLost
	}

	return nil
}

func intervalFor(duration time.Duration) string {
	return fmt.Sprintf("%d milliseconds", duration.Milliseconds())
}
