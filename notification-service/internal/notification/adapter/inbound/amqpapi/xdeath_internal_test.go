package amqpapi

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

// x-death is a list of tables, one per queue a message has been dead-lettered from,
// and every wrong way of reading it still compiles. Hence a pure function and a table
// of the shapes a broker actually sends.
func TestDeathCount(t *testing.T) {
	for name, testCase := range map[string]struct {
		headers  amqp.Table
		expected int
	}{
		"no headers at all": {
			headers:  nil,
			expected: 0,
		},
		"a first delivery has no x-death": {
			headers:  amqp.Table{},
			expected: 0,
		},
		"one rejection": {
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"queue": testWorkingQueue, "reason": "rejected", "count": int64(1)},
			}},
			expected: 1,
		},
		"several rejections": {
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"queue": testWorkingQueue, "reason": "rejected", "count": int64(3)},
			}},
			expected: 3,
		},
		// The message has also expired out of the retry queue, which gets its own
		// entry. Counting that too would double every number.
		"expiry from the retry queue is not a rejection": {
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"queue": testRetryQueue, "reason": "expired", "count": int64(2)},
				amqp.Table{"queue": testWorkingQueue, "reason": "rejected", "count": int64(2)},
			}},
			expected: 2,
		},
		"a different queue's rejections do not count": {
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"queue": "some.other.queue", "reason": "rejected", "count": int64(9)},
			}},
			expected: 0,
		},
		"a count sent as int32": {
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"queue": testWorkingQueue, "reason": "rejected", "count": int32(4)},
			}},
			expected: 4,
		},
		// Nothing in the wire format promises the width, so an unexpected one must
		// read as zero rather than panic on a type assertion.
		"an unexpected count type": {
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"queue": testWorkingQueue, "reason": "rejected", "count": "3"},
			}},
			expected: 0,
		},
		"a malformed x-death": {
			headers:  amqp.Table{"x-death": "not a list"},
			expected: 0,
		},
		"a list of things that are not tables": {
			headers:  amqp.Table{"x-death": []any{"not a table"}},
			expected: 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, testCase.expected, deathCount(testCase.headers, testWorkingQueue))
		})
	}
}

// The queue names these cases key on, derived the same way the consumer derives them.
var (
	testWorkingQueue = Subscription{Name: "order-accepted"}.Queue()
	testRetryQueue   = Subscription{Name: "order-accepted"}.RetryQueue()
)
