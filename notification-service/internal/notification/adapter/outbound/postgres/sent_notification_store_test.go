//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	testpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/adapter/outbound/postgres"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/notification/application"
	platformdatabase "github.com/mojtaba-gheytasi/go-order-system-showcase/notification-service/internal/platform/database"
)

// Long enough that it cannot expire while a test is running, even on a loaded machine
// sharing Docker with several other containers.
//
// This is not arbitrary padding. A shorter lease makes the concurrency test
// time-sensitive in a way that looks like a correctness failure: if the last goroutine
// is scheduled after the first one's lease has run out, a second claim is granted, and
// the store was right to grant it. Tests that care about expiry pass their own short
// lease explicitly.
const testLease = time.Hour

func testKey(orderID string) application.EffectKey {
	return application.EffectKey{
		SubjectID: orderID,
		Kind:      application.KindOrderConfirmation,
		Channel:   application.ChannelEmail,
	}
}

func TestClaimGrantsThenRecordsSent(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))
	key := testKey("order-1")

	state, claim, err := store.Claim(ctx, key, "event-1", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimGranted, state)
	assert.NotEmpty(t, claim.Token)
	assert.Equal(t, 0, claim.Attempts, "a fresh effect has made no attempts")

	attempts, err := store.BeginAttempt(ctx, key, claim.Token)
	require.NoError(t, err)
	assert.Equal(t, 1, attempts)

	require.NoError(t, store.MarkSent(ctx, key, claim.Token))

	// A redelivery now gets a definite answer rather than a second send.
	state, _, err = store.Claim(ctx, key, "event-1", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimAlreadySent, state)
}

// The distinction a bare "no rows" cannot express: already-sent means acknowledge the
// message, held-by-another means come back later. Conflating them drops an email.
func TestClaimDistinguishesAlreadySentFromHeldByAnother(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))
	key := testKey("order-1")

	_, first, err := store.Claim(ctx, key, "event-1", testLease)
	require.NoError(t, err)

	state, _, err := store.Claim(ctx, key, "event-2", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimHeldByAnother, state, "a live lease belongs to somebody")

	require.NoError(t, store.MarkSent(ctx, key, first.Token))

	state, _, err = store.Claim(ctx, key, "event-3", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimAlreadySent, state)
}

// The race the whole design exists for. Many workers reach for one effect at the same
// instant and exactly one may proceed — a check followed by a write would let several
// through, and each would send an email.
func TestOnlyOneConcurrentClaimIsGranted(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))
	key := testKey("order-1")

	const workers = 24

	var start sync.WaitGroup
	var finished sync.WaitGroup
	start.Add(1)

	states := make([]application.ClaimState, workers)
	errs := make([]error, workers)

	for worker := range workers {
		finished.Add(1)
		go func() {
			defer finished.Done()
			start.Wait()

			state, _, err := store.Claim(ctx, key, "event-1", testLease)
			states[worker] = state
			errs[worker] = err
		}()
	}

	start.Done()
	finished.Wait()

	granted := 0
	for worker := range workers {
		require.NoError(t, errs[worker])
		if states[worker] == application.ClaimGranted {
			granted++
		} else {
			assert.Equal(t, application.ClaimHeldByAnother, states[worker])
		}
	}

	assert.Equal(t, 1, granted, "exactly one worker may hold the claim")
}

// A worker that died mid-send leaves its claim behind. Without the lease the effect
// would stay 'sending' forever and the email would never be sent.
func TestAnExpiredLeaseCanBeReclaimed(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))
	key := testKey("order-1")

	_, abandoned, err := store.Claim(ctx, key, "event-1", 50*time.Millisecond)
	require.NoError(t, err)
	_, err = store.BeginAttempt(ctx, key, abandoned.Token)
	require.NoError(t, err)

	// Held, so nobody may take it yet.
	state, _, err := store.Claim(ctx, key, "event-2", testLease)
	require.NoError(t, err)
	require.Equal(t, application.ClaimHeldByAnother, state)

	time.Sleep(200 * time.Millisecond)

	state, reclaimed, err := store.Claim(ctx, key, "event-2", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimGranted, state)
	// The budget survives the takeover. It counts provider calls against the effect,
	// not against whoever happens to hold the claim, or a crash-looping worker would
	// retry forever.
	assert.Equal(t, 1, reclaimed.Attempts)
}

// The fence. Worker A overruns its lease, B takes over and finishes the job, and A's
// late write must not land — a 'failed' write on top of a 'sent' row would release the
// effect and cause a third email.
func TestAStaleWorkerCannotFinaliseAfterTakeover(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))
	key := testKey("order-1")

	_, stale, err := store.Claim(ctx, key, "event-1", 50*time.Millisecond)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	state, fresh, err := store.Claim(ctx, key, "event-2", testLease)
	require.NoError(t, err)
	require.Equal(t, application.ClaimGranted, state)
	require.NoError(t, store.MarkSent(ctx, key, fresh.Token))

	// The stale worker arrives late with a token that is no longer current.
	assert.ErrorIs(t, store.MarkFailed(ctx, key, stale.Token), application.ErrClaimLost)
	assert.ErrorIs(t, store.MarkSent(ctx, key, stale.Token), application.ErrClaimLost)
	_, err = store.BeginAttempt(ctx, key, stale.Token)
	assert.ErrorIs(t, err, application.ErrClaimLost)

	// And the effect is still recorded as sent, not released.
	state, _, err = store.Claim(ctx, key, "event-3", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimAlreadySent, state)
}

// A released claim can be retried immediately, without waiting out the lease.
func TestAFailedAttemptCanBeClaimedAgain(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))
	key := testKey("order-1")

	_, first, err := store.Claim(ctx, key, "event-1", testLease)
	require.NoError(t, err)
	_, err = store.BeginAttempt(ctx, key, first.Token)
	require.NoError(t, err)
	require.NoError(t, store.MarkFailed(ctx, key, first.Token))

	state, second, err := store.Claim(ctx, key, "event-1", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimGranted, state)
	assert.Equal(t, 1, second.Attempts, "the spent attempt is remembered")
	assert.NotEqual(t, first.Token, second.Token, "each claim gets a fresh fence")
}

// Kind and channel are part of the key, so a second notification about the same order
// is a different effect. Without that, adding an "order shipped" email later would be
// deduplicated away as the confirmation having already been sent.
func TestEffectsDifferByKindAndChannel(t *testing.T) {
	ctx := context.Background()
	store := postgres.NewSentNotificationStore(startPostgres(t, ctx))

	confirmation := testKey("order-1")
	_, claim, err := store.Claim(ctx, confirmation, "event-1", testLease)
	require.NoError(t, err)
	require.NoError(t, store.MarkSent(ctx, confirmation, claim.Token))

	shipped := confirmation
	shipped.Kind = "order_shipped"

	state, _, err := store.Claim(ctx, shipped, "event-2", testLease)
	require.NoError(t, err)
	assert.Equal(t, application.ClaimGranted, state, "a different kind is a different effect")
}

func startPostgres(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()

	migrationsDirectory, err := filepath.Abs("../../../../../migrations")
	require.NoError(t, err)

	upMigrations, err := filepath.Glob(filepath.Join(migrationsDirectory, "*.up.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, upMigrations)
	sort.Strings(upMigrations)

	container, err := testpostgres.Run(
		ctx,
		"postgres:18.6-alpine",
		testpostgres.WithDatabase("notifications"),
		testpostgres.WithUsername("notifications"),
		testpostgres.WithPassword("notifications_test_password"),
		testpostgres.WithInitScripts(upMigrations...),
		testpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := platformdatabase.New(ctx, platformdatabase.Config{
		URL:             connectionString,
		MaxOpenConns:    32,
		MaxIdleConns:    8,
		ConnMaxLifetime: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}
