BEGIN;
-- on_hand:  units physically in the warehouse.
-- reserved: units promised to orders
CREATE TABLE stock_items (
    product_sku TEXT        NOT NULL,
    on_hand     INTEGER     NOT NULL,
    reserved    INTEGER     NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (product_sku),

    CONSTRAINT stock_items_product_sku_not_blank
        CHECK (length(btrim(product_sku)) > 0),
    CONSTRAINT stock_items_on_hand_non_negative
        CHECK (on_hand >= 0),
    CONSTRAINT stock_items_reserved_non_negative
        CHECK (reserved >= 0),
    CONSTRAINT stock_items_reserved_within_stock
        CHECK (reserved <= on_hand)
);

CREATE TABLE reservations (
    order_id    UUID        NOT NULL,
    product_sku TEXT        NOT NULL REFERENCES stock_items (product_sku),
    quantity    INTEGER     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (order_id, product_sku),

    CONSTRAINT reservations_quantity_positive
        CHECK (quantity > 0)
);

COMMIT;
