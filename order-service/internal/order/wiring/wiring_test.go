package wiring_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/wiring"
)

func TestNewAssemblesAServedHandler(t *testing.T) {
	module, err := wiring.New(wiring.Dependencies{
		DB:     nil,
		Logger: zerolog.New(io.Discard),
		Clock:  func() time.Time { return time.Now().UTC() },
		NewID:  func() string { return "018f0f38-5a52-7a01-8000-0000000000aa" },
	})
	require.NoError(t, err)
	require.NotNil(t, module)
	require.NotNil(t, module.HTTPHandler)

	response := httptest.NewRecorder()
	module.HTTPHandler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, response.Code)
}
