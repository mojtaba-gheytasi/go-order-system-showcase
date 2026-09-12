package grpcapi

import (
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"

	inventoryv1 "github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/api/gen/inventory/v1"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/inventory-service/internal/inventory/application"
)

// Translation between the application's types and the contract's.
//
// It lives here rather than beside the generated code because the api module is
// deliberately dependency-light — order-service imports it — and because a
// module that knew about this service's application types would have to import
// inventory-service, which already requires it: a cycle. The adapter knows both
// sides, and neither side knows the adapter.

func insufficientStockDetailFrom(
	err *application.InsufficientStockError,
) *inventoryv1.InsufficientStockDetail {
	shortfalls := make([]*inventoryv1.InsufficientStockDetail_Shortfall, 0, len(err.Shortfalls))
	for _, shortfall := range err.Shortfalls {
		shortfalls = append(shortfalls, &inventoryv1.InsufficientStockDetail_Shortfall{
			ProductSku: shortfall.ProductSKU,
			Requested:  shortfall.Requested,
			Available:  shortfall.Available,
		})
	}

	return &inventoryv1.InsufficientStockDetail{Shortfalls: shortfalls}
}

func badRequestFrom(err *application.InvalidRequestError) *errdetails.BadRequest {
	violations := make([]*errdetails.BadRequest_FieldViolation, 0, len(err.Violations))
	for _, violation := range err.Violations {
		violations = append(violations, &errdetails.BadRequest_FieldViolation{
			Field:       violation.Field,
			Description: violation.Description,
		})
	}

	if err.Omitted > 0 {
		violations = append(violations, &errdetails.BadRequest_FieldViolation{
			Field:       "lines",
			Description: fmt.Sprintf("%d further violations were not reported", err.Omitted),
		})
	}

	return &errdetails.BadRequest{FieldViolations: violations}
}

func reservationLinesFrom(lines []application.Line) []*inventoryv1.ReservationLine {
	reservationLines := make([]*inventoryv1.ReservationLine, 0, len(lines))
	for _, line := range lines {
		reservationLines = append(reservationLines, &inventoryv1.ReservationLine{
			ProductSku: line.ProductSKU,
			Quantity:   line.Quantity,
		})
	}

	return reservationLines
}
