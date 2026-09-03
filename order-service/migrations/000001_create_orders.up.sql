BEGIN;

CREATE TABLE orders (
    id              UUID        PRIMARY KEY,
    customer_id     UUID        NOT NULL,
    customer_email  TEXT        NOT NULL,
    status          TEXT        NOT NULL,
    reservation_id  UUID,
    idempotency_key TEXT        NOT NULL,
    total_amount_in_cents BIGINT      NOT NULL,
    currency        TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT orders_status_valid
        CHECK (status IN ('pending', 'accepted', 'rejected', 'shipped', 'cancelled')),
    CONSTRAINT orders_customer_email_not_blank
        CHECK (length(btrim(customer_email)) > 0),
    CONSTRAINT orders_idempotency_key_not_blank
        CHECK (length(btrim(idempotency_key)) > 0),
    CONSTRAINT orders_currency_supported
        CHECK (currency IN ('EUR', 'USD')),
    CONSTRAINT orders_total_amount_in_cents_non_negative
        CHECK (total_amount_in_cents >= 0)
);

CREATE UNIQUE INDEX orders_idempotency_key_uidx ON orders (idempotency_key);
CREATE INDEX orders_customer_id_created_at_idx ON orders (customer_id, created_at DESC);

CREATE TABLE order_items (
    order_id    UUID    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    line_number INTEGER NOT NULL,
    product_sku TEXT    NOT NULL,
    quantity    INTEGER NOT NULL,
    unit_amount_in_cents BIGINT  NOT NULL,

    PRIMARY KEY (order_id, line_number),

    CONSTRAINT order_items_line_number_positive
        CHECK (line_number > 0),
    CONSTRAINT order_items_product_sku_not_blank
        CHECK (length(btrim(product_sku)) > 0),
    CONSTRAINT order_items_quantity_positive
        CHECK (quantity > 0),
    CONSTRAINT order_items_unit_amount_in_cents_non_negative
        CHECK (unit_amount_in_cents >= 0)
);

COMMIT;
