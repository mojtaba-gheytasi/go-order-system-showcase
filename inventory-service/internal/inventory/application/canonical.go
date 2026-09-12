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

	// A request can be wrong in as many places as it has lines. Reporting all
	// of them would let a caller decide the size of the error response, so the
	// report is capped and the remainder is counted instead.
	maxReportedViolations = 20
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
	report := violationReport{}

	trimmedOrderID := strings.TrimSpace(orderID)
	if _, err := uuid.Parse(trimmedOrderID); err != nil {
		report.add("order_id", "must be a uuid")
	}

	if len(lines) == 0 {
		report.add("lines", "at least one line is required")
	}

	// Past the cap the request is refused whole, so walking the lines could only
	// produce violations about a request that is already rejected.
	if len(lines) > maxLines {
		report.add("lines", fmt.Sprintf("at most %d lines are supported, got %d", maxLines, len(lines)))

		return "", nil, report.err()
	}

	quantities := make(map[string]int32, len(lines))
	for index, line := range lines {
		productSKU := strings.TrimSpace(line.ProductSKU)
		if productSKU == "" {
			report.add(fmt.Sprintf("lines[%d].product_sku", index), "must not be blank")

			continue
		}

		if line.Quantity <= 0 || line.Quantity > maxLineQuantity {
			report.add(
				fmt.Sprintf("lines[%d].quantity", index),
				fmt.Sprintf("must be between 1 and %d, got %d", maxLineQuantity, line.Quantity),
			)

			continue
		}

		if quantities[productSKU] > maxLineQuantity-line.Quantity {
			report.add(
				fmt.Sprintf("lines[%d].quantity", index),
				fmt.Sprintf("total quantity for %s exceeds %d", productSKU, maxLineQuantity),
			)

			continue
		}

		quantities[productSKU] += line.Quantity
	}

	if err := report.err(); err != nil {
		return "", nil, err
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

// violationReport collects what is wrong with a request so that all of it can be
// answered at once, and enforces the cap on how much of it is reported back.
type violationReport struct {
	violations []FieldViolation
	omitted    int
}

func (report *violationReport) add(field string, description string) {
	if len(report.violations) >= maxReportedViolations {
		report.omitted++

		return
	}

	report.violations = append(
		report.violations,
		FieldViolation{Field: field, Description: description},
	)
}

func (report *violationReport) err() error {
	if len(report.violations) == 0 {
		return nil
	}

	return &InvalidRequestError{Violations: report.violations, Omitted: report.omitted}
}
