// Package catalogstub is a temporary in-process product catalog. It owns the
// authoritative prices used while creating orders until a real product catalog
// adapter replaces it.
package catalogstub

import (
	"context"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type Catalog struct {
	prices map[string]domain.Money
}

var _ application.ProductCatalog = (*Catalog)(nil)

// New builds the demo catalogue. These product SKUs deliberately match the ones
// deploy/inventory/dev-seed.sql stocks: a product priced here but unknown to
// inventory would be rejected by the catalogue before the reservation is ever
// attempted, which hides the paths worth demonstrating.
//
// SKU-SCARCE is stocked in single figures so the out-of-stock response is
// reachable by hand.
func New() *Catalog {
	return &Catalog{prices: map[string]domain.Money{
		"SKU-A":      {AmountInCents: 1250, Currency: "EUR"},
		"SKU-B":      {AmountInCents: 500, Currency: "EUR"},
		"SKU-SCARCE": {AmountInCents: 9900, Currency: "EUR"},
	}}
}

func (catalog *Catalog) Prices(
	ctx context.Context,
	productSKUs []string,
) (map[string]domain.Money, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prices := make(map[string]domain.Money, len(productSKUs))
	missing := make([]string, 0)
	for _, productSKU := range productSKUs {
		price, found := catalog.prices[productSKU]
		if found == false {
			missing = append(missing, productSKU)

			continue
		}

		prices[productSKU] = price
	}

	// Every unpriced product is named, not just the first. A customer fixing a
	// basket one sku per attempt is the thing this avoids.
	if len(missing) > 0 {
		return nil, &application.ProductNotFoundError{ProductSKUs: missing}
	}

	return prices, nil
}
