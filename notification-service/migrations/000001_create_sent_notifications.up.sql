-- sent_notifications records which notification effects have happened, and which
-- one a worker is currently attempting.
--
-- The table is keyed by the EFFECT, not by the event that triggered it. Two events
-- can describe the same effect — a republished outbox row, a redelivery — and what
-- must not happen twice is the email.
--
-- The effect is three typed columns with a composite primary key rather than one
-- concatenated string. A single text key would need a delimiter, and a delimiter
-- that can appear inside an order id makes two different effects collide.
CREATE TABLE sent_notifications (
    subject_id TEXT NOT NULL,
    -- Which notification. Part of the key so that adding a second kind later --
    -- "order shipped" -- does not share an identity with the confirmation and get
    -- deduplicated away as already sent.
    notification_kind TEXT NOT NULL,
    channel TEXT NOT NULL,

    -- The event occurrence that most recently drove this effect. Audit only: it is
    -- deliberately not part of the key.
    event_id TEXT NOT NULL,

    state TEXT NOT NULL,

    -- The fencing token of the current claim.
    --
    -- Every state write carries it. Without it a worker whose lease expired could
    -- overwrite the result of the worker that took over: A stalls, B reclaims and
    -- sends, then A's late write marks the effect failed and a third send happens.
    claim_token TEXT NOT NULL,

    -- How many times the email provider has been called for this effect. This is
    -- the retry budget, and it lives here rather than being read from RabbitMQ's
    -- x-death header: x-death counts trips through the retry queue, and a trip can
    -- happen without the provider ever being called.
    attempts INTEGER NOT NULL DEFAULT 0,

    -- When the current claim stops being valid. A worker that dies mid-send leaves
    -- its claim behind; without an expiry the effect would stay 'sending' forever
    -- and the email would never be sent.
    lease_expires_at TIMESTAMPTZ NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT sent_notifications_pkey
        PRIMARY KEY (subject_id, notification_kind, channel),

    -- Spelled out so an unexpected value cannot be written at all. 'failed' is a
    -- released claim, not a dead end: the effect can be claimed again.
    CONSTRAINT sent_notifications_state_known
        CHECK (state IN ('sending', 'sent', 'failed')),

    CONSTRAINT sent_notifications_attempts_not_negative
        CHECK (attempts >= 0)
);

-- There is deliberately no customer_email column. Deduplication does not need it,
-- and leaving it out keeps every customer email address out of this database
-- entirely -- the addresses live in the message bodies the broker holds, and
-- nowhere else in this service.

-- Finding what is stuck: claims whose lease has expired, and anything parked in
-- 'failed'. Partial, because 'sent' is the overwhelming majority of the table and
-- is never the answer to "what needs attention?"
CREATE INDEX sent_notifications_unfinished_idx
    ON sent_notifications (lease_expires_at)
    WHERE state <> 'sent';
