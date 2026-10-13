BEGIN;

CREATE TABLE outbox_events (
    event_id        UUID        PRIMARY KEY,
    event_type      TEXT        NOT NULL,
    order_id        UUID        NOT NULL REFERENCES orders (id),

    -- The event as it was at the moment of the change. Rebuilding it from the order
    -- later would describe the order as it is then, not as it was.
    payload         JSONB       NOT NULL,
    correlation_id  TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL DEFAULT 'pending',
    -- Failed publications that count toward the retry budget. A broker that cannot
    -- be reached at all does not count: that is an outage, not a problem with
    -- this event.
    attempts        INTEGER     NOT NULL DEFAULT 0,

    -- When the row may next be claimed. It is both the retry backoff and the claim
    -- lease: claiming pushes it forward, so other workers skip the row, and a worker
    -- that dies mid-publish leaves a row that becomes due again on its own.
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Says which worker is publishing this row right now. (A worker is the
    -- background loop in order-service that publishes these events; each running
    -- copy of the service has one.) Each claim writes a new random value here, and
    -- the worker keeps a copy. Its later updates that reschedule the row, or stop
    -- retrying it, only apply if the value still matches its copy.
    --
    -- Why: worker A claims the row and freezes for longer than its lease. Worker B
    -- then claims the same row, which writes a new value here. When A wakes up and
    -- tries to schedule a retry, its copy no longer matches, so its update changes
    -- nothing and does not move B's retry time.
    claim_token     UUID,

    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,
    needs_attention_at TIMESTAMPTZ,

    CONSTRAINT outbox_events_type_valid
        CHECK (event_type IN ('order_accepted')),
    -- 'needs_attention' is where automatic retries stop. The row is kept, and a
    -- person decides when to put it back to 'pending'.
    CONSTRAINT outbox_events_status_valid
        CHECK (status IN ('pending', 'published', 'needs_attention')),
    CONSTRAINT outbox_events_attempts_non_negative
        CHECK (attempts >= 0),
    CONSTRAINT outbox_events_one_per_order
        UNIQUE (order_id, event_type)
);

CREATE INDEX outbox_events_due_idx ON outbox_events (next_attempt_at)
    WHERE status = 'pending';

COMMIT;
