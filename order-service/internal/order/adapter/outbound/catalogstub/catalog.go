// Package catalogstub is a temporary in-process product catalog. It owns the
// authoritative prices used while creating orders until a real product catalog
// adapter replaces it.
package catalogstub

import (
	"context"
	"fmt"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

type Catalog struct {
	prices map[string]domain.Money
}

var _ application.ProductCatalog = (*Catalog)(nil)

func New() *Catalog {
	return &Catalog{prices: map[string]domain.Money{
		"SKU-A": {AmountInCents: 1250, Currency: "EUR"},
		"SKU-B": {AmountInCents: 500, Currency: "EUR"},
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
	for _, productSKU := range productSKUs {
		price, found := catalog.prices[productSKU]
		if found == false {
			return nil, fmt.Errorf("%w: %s", application.ErrProductNotFound, productSKU)
		}

		prices[productSKU] = price
	}

	return prices, nil
}
