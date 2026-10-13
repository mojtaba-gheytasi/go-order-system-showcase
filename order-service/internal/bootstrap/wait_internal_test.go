package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWaitForReportsWhetherTheWorkerStoppedInTime(t *testing.T) {
	stopped := make(chan struct{})
	close(stopped)
	assert.True(t, waitFor(stopped, time.Second))

	stuck := make(chan struct{})
	assert.False(t, waitFor(stuck, 10*time.Millisecond))
}
