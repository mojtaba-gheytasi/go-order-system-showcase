package application

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
)

// MaxPublishAttempts is how many failed publications an event may have before
// automatic retries stop and it waits for a person.
const MaxPublishAttempts = 10

const (
	publishRetryInitialDelay = 5 * time.Second
	publishRetryMaxDelay     = 5 * time.Minute
	outboxWriteTimeout       = 5 * time.Second

	// Published events are kept for a month, so "did this order's event go out,
	// and when?" can still be answered, and then deleted so the table does not
	// grow forever. Events that need attention are never deleted.
	publishedEventRetention = 30 * 24 * time.Hour
	outboxCleanupInterval   = time.Hour

	// Deleting in batches keeps each statement short, so it never holds locks
	// for long while orders are being accepted.
	outboxCleanupBatchSize = 1000
)

// PublishOrderEvents is the outbox worker: it publishes the events that order
// creation stored, one at a time, in the background.
//
// A failure is handled by what it says about the event:
//
//	broker unreachable          an outage, not this event's fault: release the row
//	                            without using budget and pause the whole worker
//	not routed / rejected /     use one attempt and retry the row later; after
//	outcome unknown             MaxPublishAttempts it needs attention
//	event cannot be published   retrying cannot help: it needs attention at once
type PublishOrderEvents struct {
	outbox       EventOutbox
	events       OrderEventPublisher
	lease        time.Duration
	pollInterval time.Duration

	// random returns a value in [0, n). Injected so tests can fix the jitter.
	random func(n int64) int64
	clock  Clock
	logger zerolog.Logger

	// Only the goroutine running Run touches the fields below.
	//
	// outages counts consecutive attempts that could not reach the broker.
	outages int

	// lastCleanup is when the current cleanup round started, and
	// cleanupUnfinished says that round's last batch was full.
	lastCleanup       time.Time
	cleanupUnfinished bool
}

func NewPublishOrderEvents(
	outbox EventOutbox,
	events OrderEventPublisher,
	lease time.Duration,
	pollInterval time.Duration,
	random func(n int64) int64,
	clock Clock,
	logger zerolog.Logger,
) *PublishOrderEvents {
	return &PublishOrderEvents{
		outbox:       outbox,
		events:       events,
		lease:        lease,
		pollInterval: pollInterval,
		random:       random,
		clock:        clock,
		logger:       logger,
	}
}

// Run publishes due events until ctx is cancelled. An event already claimed when
// that happens is still finished.
func (useCase *PublishOrderEvents) Run(ctx context.Context) {
	for ctx.Err() == nil {
		pause := useCase.publishNext(ctx)
		if pause == 0 {
			continue
		}

		if err := waitForRetry(ctx, pause); err != nil {
			return
		}
	}
}

// publishNext handles at most one event and returns how long to wait before
// claiming the next one.
func (useCase *PublishOrderEvents) publishNext(ctx context.Context) time.Duration {
	entry, err := useCase.outbox.ClaimNext(ctx, useCase.lease)
	switch {
	case err == nil:
	case errors.Is(err, ErrOutboxEmpty):
		useCase.deleteOldPublishedEvents(ctx)

		return useCase.pollInterval
	case errors.Is(err, ErrEventUnpublishable):
		useCase.setAside(context.WithoutCancel(ctx), entry, false, err)

		return 0
	default:
		if ctx.Err() == nil {
			useCase.logger.Warn().Err(err).Msg("claim outbox event")
		}

		return useCase.pollInterval
	}

	// Detached from shutdown on purpose: a publication cut short leaves its
	// outcome unknown, and a claimed event whose outcome is never written waits
	// out its whole lease before anyone retries it.
	workCtx := context.WithoutCancel(ctx)
	publishErr := useCase.events.PublishOrderAccepted(workCtx, entry.Event, entry.CorrelationID)

	return useCase.settle(workCtx, entry, publishErr)
}

func (useCase *PublishOrderEvents) settle(
	ctx context.Context,
	entry OutboxEntry,
	publishErr error,
) time.Duration {
	if errors.Is(publishErr, ErrPublisherUnavailable) {
		useCase.outages++
		pause := retryDelay(useCase.outages, useCase.random)

		useCase.logEntry(useCase.logger.Warn(), entry).
			Err(publishErr).
			Dur("pause", pause).
			Msg("event broker unreachable; pausing the worker")
		useCase.write(ctx, entry, "release outbox event", func(writeCtx context.Context) error {
			return useCase.outbox.Release(writeCtx, entry, pause)
		})

		return pause
	}

	useCase.outages = 0

	switch {
	case publishErr == nil:
		useCase.write(ctx, entry, "mark outbox event published", func(writeCtx context.Context) error {
			return useCase.outbox.MarkPublished(writeCtx, entry.EventID)
		})

		return 0
	case errors.Is(publishErr, ErrEventUnpublishable):
		useCase.setAside(ctx, entry, false, publishErr)

		return 0
	}

	attempt := entry.Attempts + 1
	if attempt >= MaxPublishAttempts {
		useCase.setAside(ctx, entry, true, publishErr)

		return useCase.pollInterval
	}

	delay := retryDelay(attempt, useCase.random)
	message := "event not published; will retry"
	if errors.Is(publishErr, ErrPublicationOutcomeUnknown) {
		message = "event publication outcome unknown; retrying may publish it twice"
	}

	useCase.logEntry(useCase.logger.Warn(), entry).
		Err(publishErr).
		Int("attempt", attempt).
		Dur("retry_in", delay).
		Msg(message)
	useCase.write(ctx, entry, "schedule outbox event retry", func(writeCtx context.Context) error {
		return useCase.outbox.ScheduleRetry(writeCtx, entry, delay, publishErr.Error())
	})

	return useCase.pollInterval
}

// setAside stops automatic retries for an event. The row is kept; this log line
// is what tells a person it is there.
func (useCase *PublishOrderEvents) setAside(
	ctx context.Context,
	entry OutboxEntry,
	countAttempt bool,
	cause error,
) {
	attempts := entry.Attempts
	if countAttempt {
		attempts++
	}

	useCase.logEntry(useCase.logger.Error(), entry).
		Err(cause).
		Int("attempts", attempts).
		Msg("event needs attention; automatic retries stopped")
	useCase.write(ctx, entry, "mark outbox event as needing attention", func(writeCtx context.Context) error {
		return useCase.outbox.MarkNeedsAttention(writeCtx, entry, countAttempt, cause.Error())
	})
}

// write records the outcome of one attempt. A failure here is logged and nothing
// more: the row's lease runs out and the event is tried again, which can publish
// it twice but cannot lose it.
func (useCase *PublishOrderEvents) write(
	ctx context.Context,
	entry OutboxEntry,
	action string,
	write func(context.Context) error,
) {
	writeCtx, cancel := context.WithTimeout(ctx, outboxWriteTimeout)
	defer cancel()

	err := write(writeCtx)
	switch {
	case err == nil:
	case errors.Is(err, ErrOutboxClaimLost):
		useCase.logEntry(useCase.logger.Info(), entry).
			Msg("outbox event was claimed by another worker; leaving it to them")
	default:
		useCase.logEntry(useCase.logger.Warn(), entry).Err(err).Msg(action)
	}
}

func (useCase *PublishOrderEvents) logEntry(event *zerolog.Event, entry OutboxEntry) *zerolog.Event {
	return event.
		Str("event_id", entry.EventID).
		Str("order_id", string(entry.Event.OrderID))
}

// deleteOldPublishedEvents runs only when there is nothing to publish, and
// deletes at most one batch per call. A backlog is worked through one batch per
// idle poll, with a claim in between each, so a new event waits for one short,
// time-limited statement at most, never for the whole backlog.
func (useCase *PublishOrderEvents) deleteOldPublishedEvents(ctx context.Context) {
	now := useCase.clock()
	if useCase.cleanupUnfinished == false {
		if now.Sub(useCase.lastCleanup) < outboxCleanupInterval {
			return
		}
		useCase.lastCleanup = now
	}

	deleteCtx, cancel := context.WithTimeout(ctx, outboxWriteTimeout)
	defer cancel()

	deleted, err := useCase.outbox.DeletePublished(deleteCtx, publishedEventRetention, outboxCleanupBatchSize)
	if err != nil {
		useCase.cleanupUnfinished = false
		if ctx.Err() == nil {
			useCase.logger.Warn().Err(err).Msg("delete old published outbox events")
		}

		return
	}

	// A full batch means there may be more; the next idle poll carries on.
	useCase.cleanupUnfinished = deleted == outboxCleanupBatchSize
	if deleted > 0 {
		useCase.logger.Info().Int("deleted", deleted).Msg("deleted old published outbox events")
	}
}

// retryDelay doubles from the initial delay up to the cap, then picks a random
// point in the upper half of it. Without the randomness, events that failed
// together would all retry at the same moment.
func retryDelay(attempt int, random func(n int64) int64) time.Duration {
	delay := publishRetryInitialDelay
	for step := 1; step < attempt && delay < publishRetryMaxDelay; step++ {
		delay *= 2
	}
	delay = min(delay, publishRetryMaxDelay)

	half := delay / 2

	return half + time.Duration(random(int64(half)+1))
}
