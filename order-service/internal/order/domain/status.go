package domain

type Status string

const (
	StatusPending   Status = "pending"
	StatusAccepted  Status = "accepted"
	StatusRejected  Status = "rejected"
	StatusShipped   Status = "shipped"
	StatusCancelled Status = "cancelled"
)

func (status Status) isValid() bool {
	switch status {
	case StatusPending, StatusAccepted, StatusRejected, StatusShipped, StatusCancelled:
		return true
	default:
		return false
	}
}

func (status Status) canTransitionTo(next Status) bool {
	switch status {
	case StatusPending:
		return next == StatusAccepted || next == StatusRejected || next == StatusCancelled
	case StatusAccepted:
		return next == StatusShipped || next == StatusCancelled
	default:
		return false
	}
}
