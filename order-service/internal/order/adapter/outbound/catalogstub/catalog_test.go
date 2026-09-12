package catalogstub_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/outbound/catalogstub"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

func TestCatalogReturnsAuthoritativePrices(t *testing.T) {
	prices, err := catalogstub.New().Prices(context.Background(), []string{"SKU-A", "SKU-B"})
	require.NoError(t, err)

	assert.Equal(t, domain.Money{AmountInCents: 1250, Currency: "EUR"}, prices["SKU-A"])
	assert.Equal(t, domain.Money{AmountInCents: 500, Currency: "EUR"}, prices["SKU-B"])
}

func TestCatalogRejectsAnUnknownProduct(t *testing.T) {
	_, err := catalogstub.New().Prices(context.Background(), []string{"NOT-A-PRODUCT"})

	require.ErrorIs(t, err, application.ErrProductNotFound)
}

// Naming one unpriced product at a time would cost a customer an attempt per bad
// sku, so the refusal names all of them.
func TestCatalogNamesEveryUnknownProduct(t *testing.T) {
	_, err := catalogstub.New().Prices(
		context.Background(),
		[]string{"SKU-A", "NOT-A-PRODUCT", "ALSO-NOT-A-PRODUCT"},
	)

	var notFound *application.ProductNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, []string{"NOT-A-PRODUCT", "ALSO-NOT-A-PRODUCT"}, notFound.ProductSKUs)
}
