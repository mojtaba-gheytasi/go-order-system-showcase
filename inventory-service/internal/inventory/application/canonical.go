package application

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const (
	maxLineQuantity = 10_000
	maxLines        = 200
)

type Line struct {
	ProductSKU string
	Quantity   int32
}

// Canonicalize validates a request and reduces it to one form, so that two
// callers asking for the same thing in different words produce identical lines.
//
// Two properties matter downstream and both come from here:
//
//   - Summing duplicate SKUs makes (order_id, product_sku) a usable primary key,
//     and means a request cannot lock the same row twice.
//   - Sorting by SKU fixes the order stock rows are locked in. Without it, one
//     request reserving [A, B] and another reserving [B, A] deadlock.
func Canonicalize(orderID string, lines []Line) (string, []Line, error) {
	trimmedOrderID := strings.TrimSpace(orderID)
	if _, err := uuid.Parse(trimmedOrderID); err != nil {
		return "", nil, fmt.Errorf("%w: order id must be a uuid", ErrInvalidRequest)
	}

	if len(lines) == 0 {
		return "", nil, fmt.Errorf("%w: at least one line is required", ErrInvalidRequest)
	}

	if len(lines) > maxLines {
		return "", nil, fmt.Errorf(
			"%w: at most %d lines are supported, got %d",
			ErrInvalidRequest,
			maxLines,
			len(lines),
		)
	}

	quantities := make(map[string]int32, len(lines))
	for index, line := range lines {
		productSKU := strings.TrimSpace(line.ProductSKU)
		if productSKU == "" {
			return "", nil, fmt.Errorf(
				"%w: line %d has a blank product sku",
				ErrInvalidRequest,
				index+1,
			)
		}

		if line.Quantity <= 0 || line.Quantity > maxLineQuantity {
			return "", nil, fmt.Errorf(
				"%w: line %d quantity must be between 1 and %d, got %d",
				ErrInvalidRequest,
				index+1,
				maxLineQuantity,
				line.Quantity,
			)
		}

		if quantities[productSKU] > maxLineQuantity-line.Quantity {
			return "", nil, fmt.Errorf(
				"%w: total quantity for %s exceeds %d",
				ErrInvalidRequest,
				productSKU,
				maxLineQuantity,
			)
		}

		quantities[productSKU] += line.Quantity
	}

	canonical := make([]Line, 0, len(quantities))
	for productSKU, quantity := range quantities {
		canonical = append(canonical, Line{ProductSKU: productSKU, Quantity: quantity})
	}

	slices.SortFunc(canonical, func(first, second Line) int {
		return strings.Compare(first.ProductSKU, second.ProductSKU)
	})

	return trimmedOrderID, canonical, nil
}
