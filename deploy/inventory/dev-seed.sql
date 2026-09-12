-- Development stock, applied by the inventory-seed container after migrations.
--
-- Deliberately not a migration: a migration runs everywhere the schema is
-- applied, including production, where this data is fiction. Schema and seed
-- data have different lifetimes and belong in different places. Keeping it in
-- deploy/ means the only thing that ever applies it is Docker Compose.
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
