-- Development stock, applied by the inventory-seed container after migrations.
--
-- Deliberately not a migration, and deliberately sitting next to migrations/ so
-- the difference is visible from here: a migration runs everywhere the schema is
-- applied, including production, where this data is fiction. Schema and seed data
-- have different lifetimes.
--
-- What keeps this out of production is not where it lives but what reads it:
-- golang-migrate is pointed at migrations/ and nothing else, and the only thing
-- that ever applies this file is the inventory-seed container in Docker Compose.
-- It lives beside the schema it seeds because the two change together -- adding a
-- column here means editing both -- and because a reader of this service should be
-- able to see both without knowing the repository root.
--
-- The product SKUs match order-service's catalogstub, so an order placed against
-- the demo catalogue can actually be reserved here. SKU-SCARCE exists to make
-- the out-of-stock path reachable by hand.
--
-- Idempotent, because the seed container reruns on every `make up`. Existing
-- stock levels are left alone rather than reset, so a demo that has already
-- reserved units is not silently rewound underneath it.
INSERT INTO stock_items (product_sku, on_hand) VALUES
    ('SKU-A', 100),
    ('SKU-B', 25),
    ('SKU-SCARCE', 3)
ON CONFLICT (product_sku) DO NOTHING;
