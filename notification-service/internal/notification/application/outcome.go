package application

// Outcome is what handling one delivery concluded.
//
// Most of these are not failures, and the transport acts differently on each.
// Collapsing them into error/no-error is how a consumer ends up acknowledging a message
// it never handled, or burning a retry on work another worker is already doing.
type Outcome int

const (
	// OutcomeUnknown is never returned. It exists so a struct that forgot to set an
	// outcome does not read as Sent.
	OutcomeUnknown Outcome = iota

	OutcomeSent
	OutcomeAlreadySent

	// OutcomeClaimBusy means another worker holds the claim. Not a failure, and not a
	// spent send attempt.
	OutcomeClaimBusy

	// OutcomeInvalid means the message will never be processable. Parked immediately.
	OutcomeInvalid

	OutcomeTransientFailure
	OutcomeStorageFailure

	// OutcomeExhausted means the send budget is spent. The sender is not called again.
	OutcomeExhausted

	// OutcomeShuttingDown means the process did not start this work.
	OutcomeShuttingDown
)

func (outcome Outcome) String() string {
	switch outcome {
	case OutcomeSent:
		return "sent"
	case OutcomeAlreadySent:
		return "already_sent"
	case OutcomeClaimBusy:
		return "claim_busy"
	case OutcomeInvalid:
		return "invalid"
	case OutcomeTransientFailure:
		return "transient_failure"
	case OutcomeStorageFailure:
		return "storage_failure"
	case OutcomeExhausted:
		return "exhausted"
	case OutcomeShuttingDown:
		return "shutting_down"
	default:
		return "unknown"
	}
}

type Result struct {
	Outcome Outcome

	// Attempts counts provider calls made for this effect, including this one.
	Attempts int

	// Err carries the cause for logging. The transport decides from Outcome, never
	// from this.
	Err error
}
