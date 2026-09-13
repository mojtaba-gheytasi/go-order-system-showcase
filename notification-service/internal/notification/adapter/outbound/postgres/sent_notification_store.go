// Package postgres records which notification effects have happened.
//
// Deciding whether to send is a contention problem, and a read followed by a write
// leaves a gap in which two workers both see "not sent" and both send. So the check and
// the write are one statement — the shape inventory uses for the last unit of stock.
package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
)

type SentNotificationStore struct {
	db *sql.DB
}

var _ application.SentNotificationStore = (*SentNotificationStore)(nil)

func NewSentNotificationStore(db *sql.DB) *SentNotificationStore {
	return &SentNotificationStore{db: db}
}

// The WHERE on the conflict branch is what makes it safe: a row is taken over only when
// the previous attempt released it or its lease expired.
//
// Zero rows has two meanings — acknowledge, or come back later — and treating the second
// as the first would drop the email, so the state is read back rather than guessed.
func (store *SentNotificationStore) Claim(
	ctx context.Context,
	key application.EffectKey,
	eventID string,
	lease time.Duration,
) (application.ClaimState, application.Claim, error) {
	token, err := newClaimToken()
	if err != nil {
		return 0, application.Claim{}, err
	}

	const claimStatement = `
		INSERT INTO sent_notifications (
			subject_id, notification_kind, channel,
			event_id, state, claim_token, attempts, lease_expires_at
		)
		VALUES ($1, $2, $3, $4, 'sending', $5, 0, now() + $6::interval)
		ON CONFLICT (subject_id, notification_kind, channel) DO UPDATE
		   SET state = 'sending',
		       claim_token = EXCLUDED.claim_token,
		       event_id = EXCLUDED.event_id,
		       lease_expires_at = EXCLUDED.lease_expires_at,
		       updated_at = now()
		 WHERE sent_notifications.state = 'failed'
		    OR (sent_notifications.state = 'sending'
		        AND sent_notifications.lease_expires_at < now())
		RETURNING attempts`

	var attempts int
	err = store.db.QueryRowContext(
		ctx,
		claimStatement,
		key.SubjectID,
		string(key.Kind),
		string(key.Channel),
		eventID,
		token,
		intervalFor(lease),
	).Scan(&attempts)

	switch {
	case err == nil:
		return application.ClaimGranted, application.Claim{Token: token, Attempts: attempts}, nil
	case errors.Is(err, sql.ErrNoRows) == false:
		return 0, application.Claim{}, fmt.Errorf("claim notification: %w", err)
	}

	// ON CONFLICT blocks rather than erroring while a competing transaction is in
	// flight, so by here the winner has committed.
	state, err := store.currentState(ctx, key)
	if err != nil {
		return 0, application.Claim{}, err
	}

	switch state {
	case "sent":
		return application.ClaimAlreadySent, application.Claim{}, nil
	case "sending":
		return application.ClaimHeldByAnother, application.Claim{}, nil
	default:
		// 'failed' should have been taken over above, so something changed the row.
		// Reporting it held sends the delivery back, which is the safe direction.
		return application.ClaimHeldByAnother, application.Claim{}, nil
	}
}

// Separate from Claim so the counter moves only when a provider call is about to happen,
// and committed before the send so an attempt lost to a crash is still counted.
func (store *SentNotificationStore) BeginAttempt(
	ctx context.Context,
	key application.EffectKey,
	token string,
) (int, error) {
	const beginStatement = `
		UPDATE sent_notifications
		   SET attempts = attempts + 1,
		       updated_at = now()
		 WHERE subject_id = $1
		   AND notification_kind = $2
		   AND channel = $3
		   AND state = 'sending'
		   AND claim_token = $4
		RETURNING attempts`

	var attempts int
	err := store.db.QueryRowContext(
		ctx,
		beginStatement,
		key.SubjectID,
		string(key.Kind),
		string(key.Channel),
		token,
	).Scan(&attempts)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, application.ErrClaimLost
	}
	if err != nil {
		return 0, fmt.Errorf("begin notification attempt: %w", err)
	}

	return attempts, nil
}

func (store *SentNotificationStore) MarkSent(
	ctx context.Context,
	key application.EffectKey,
	token string,
) error {
	return store.finish(ctx, key, token, "sent")
}

// Releases the claim so the effect can be retried without waiting out the lease.
func (store *SentNotificationStore) MarkFailed(
	ctx context.Context,
	key application.EffectKey,
	token string,
) error {
	return store.finish(ctx, key, token, "failed")
}

// The token in the WHERE is the fence: a worker that overran its lease finds zero rows
// and learns it lost the effect, instead of a 'failed' write landing on a 'sent' row and
// causing a third email.
func (store *SentNotificationStore) finish(
	ctx context.Context,
	key application.EffectKey,
	token string,
	state string,
) error {
	const finishStatement = `
		UPDATE sent_notifications
		   SET state = $5,
		       updated_at = now()
		 WHERE subject_id = $1
		   AND notification_kind = $2
		   AND channel = $3
		   AND state = 'sending'
		   AND claim_token = $4`

	result, err := store.db.ExecContext(
		ctx,
		finishStatement,
		key.SubjectID,
		string(key.Kind),
		string(key.Channel),
		token,
		state,
	)
	if err != nil {
		return fmt.Errorf("mark notification %s: %w", state, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark notification %s: %w", state, err)
	}

	if affected == 0 {
		return application.ErrClaimLost
	}

	return nil
}

func (store *SentNotificationStore) currentState(
	ctx context.Context,
	key application.EffectKey,
) (string, error) {
	const stateQuery = `
		SELECT state
		  FROM sent_notifications
		 WHERE subject_id = $1
		   AND notification_kind = $2
		   AND channel = $3`

	var state string
	err := store.db.QueryRowContext(
		ctx,
		stateQuery,
		key.SubjectID,
		string(key.Kind),
		string(key.Channel),
	).Scan(&state)
	if err != nil {
		return "", fmt.Errorf("read notification state: %w", err)
	}

	return state, nil
}

// Random rather than a counter, so a restarted process cannot reuse one.
func newClaimToken() (string, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("generate claim token: %w", err)
	}

	return hex.EncodeToString(token), nil
}

// Milliseconds, so sub-second leases survive the round trip.
func intervalFor(lease time.Duration) string {
	return fmt.Sprintf("%d milliseconds", lease.Milliseconds())
}
