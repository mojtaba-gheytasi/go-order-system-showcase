package amqpapi

import amqp "github.com/rabbitmq/amqp091-go"

// deathCount reports how many times a delivery has been rejected out of the given
// queue, for logging.
//
// NOT the retry budget. x-death counts trips through the retry queue, and a trip can
// happen without the provider ever being called — when another worker holds the claim,
// for instance — so a budget keyed on it could be spent having sent nothing. The budget
// is the attempts column in the database.
func deathCount(headers amqp.Table, queue string) int {
	deaths, found := headers["x-death"].([]any)
	if found == false {
		return 0
	}

	for _, death := range deaths {
		entry, isTable := death.(amqp.Table)
		if isTable == false {
			continue
		}

		// Both, because a message that expired out of the retry queue has its own
		// entry and counting it would double every number.
		if entry["queue"] != queue || entry["reason"] != "rejected" {
			continue
		}

		// The wire format does not promise a width.
		switch count := entry["count"].(type) {
		case int64:
			return int(count)
		case int32:
			return int(count)
		case int:
			return count
		}
	}

	return 0
}
