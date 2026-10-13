package rabbitmq

import (
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestDrainReturnsWhenTheReturnsChannelIsClosed(t *testing.T) {
	returns := make(chan amqp.Return, 1)
	returns <- amqp.Return{ReplyText: "left over"}
	close(returns)

	done := make(chan struct{})
	go func() {
		drain(returns)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drain kept reading a closed channel")
	}
}
