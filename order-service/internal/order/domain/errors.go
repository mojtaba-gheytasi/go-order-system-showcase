package domain

import "errors"

var (
	ErrInvalidOrder            = errors.New("invalid order")
	ErrInvalidOrderItem        = errors.New("invalid order item")
	ErrInvalidMoney            = errors.New("invalid money")
	ErrInvalidStatusTransition = errors.New("invalid order status transition")
)
