package application

import "errors"

// ErrClaimLost means a write carried a claim that is no longer current: the lease
// expired and another worker took the effect over. Normal, not a fault.
var ErrClaimLost = errors.New("notification claim no longer held")

// ErrInvalidEvent means the event will never be processable, so it is parked rather
// than retried.
var ErrInvalidEvent = errors.New("invalid order accepted event")
