package application

import (
	"context"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

// ProductCatalog supplies authoritative product prices. Create-order callers
// provide only product identities and quantities; prices never cross the
// inbound HTTP boundary from the customer.
type ProductCatalog interface {
	Prices(
		ctx context.Context,
		productSKUs []string,
	) (map[string]domain.Money, error)
}
