package httpgin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/adapter/inbound/httpgin"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/application"
	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/order/domain"
)

// gin.SetMode is process-global, so it is set once here rather than per test.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

const validBody = `{
  "customer_id": "018f0f38-5a52-7a01-8000-000000000020",
  "customer_email": "customer@example.com",
  "items": [
    {"product_sku": "SKU-A", "quantity": 2},
    {"product_sku": "SKU-B", "quantity": 1}
  ]
}`

func TestCreateOrderReturns201WithTheCreatedOrder(t *testing.T) {
	order := testOrder(t, true)
	response := postOrder(t, &fakeUseCase{
		result: application.CreateOrderResult{Order: order, Created: true},
	}, validBody, withIdempotencyKey("demo-1"))

	require.Equal(t, http.StatusCreated, response.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "accepted", body["status"])
	assert.NotContains(t, body, "reservation_id")
	assert.NotContains(t, body, "idempotency_key")
	assert.Equal(t, float64(3000), body["total"].(map[string]any)["amount_in_cents"])

	items := body["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, float64(1), items[0].(map[string]any)["line_number"])
	assert.Equal(t, "SKU-A", items[0].(map[string]any)["product_sku"])
	assert.Equal(t, float64(2), items[1].(map[string]any)["line_number"])
}

func TestCreateOrderReturns200OnIdempotentReplay(t *testing.T) {
	order := testOrder(t, true)
	response := postOrder(t, &fakeUseCase{
		result: application.CreateOrderResult{Order: order, Created: false},
	}, validBody, withIdempotencyKey("demo-1"))

	assert.Equal(t, http.StatusOK, response.Code)
}

func TestCreateOrderTrimsTheIdempotencyKey(t *testing.T) {
	useCase := &fakeUseCase{
		result: application.CreateOrderResult{Order: testOrder(t, true), Created: true},
	}

	postOrder(t, useCase, validBody, withIdempotencyKey("  demo-1  "))

	assert.Equal(t, "demo-1", useCase.command.IdempotencyKey)
}

func TestCreateOrderRejectsBadRequests(t *testing.T) {
	tests := map[string]struct {
		body           string
		idempotencyKey string
		wantMessage    string
	}{
		"missing idempotency key": {
			body:           validBody,
			idempotencyKey: "",
			wantMessage:    "Idempotency-Key",
		},
		"blank idempotency key": {
			body:           validBody,
			idempotencyKey: "   ",
			wantMessage:    "Idempotency-Key",
		},
		"malformed json": {
			body:           `{"customer_id":`,
			idempotencyKey: "demo-1",
			wantMessage:    "could not be parsed",
		},
		"customer id is not a uuid": {
			body:           `{"customer_id":"abc","customer_email":"c@example.com","items":[{"product_sku":"SKU-A","quantity":1}]}`,
			idempotencyKey: "demo-1",
			wantMessage:    "customer_id",
		},
		"no items": {
			body:           `{"customer_id":"018f0f38-5a52-7a01-8000-000000000020","customer_email":"c@example.com","items":[]}`,
			idempotencyKey: "demo-1",
			wantMessage:    "items",
		},
		"non-positive quantity": {
			body:           `{"customer_id":"018f0f38-5a52-7a01-8000-000000000020","customer_email":"c@example.com","items":[{"product_sku":"SKU-A","quantity":0}]}`,
			idempotencyKey: "demo-1",
			wantMessage:    "quantity",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			useCase := &fakeUseCase{}
			response := postOrder(t, useCase, testCase.body, withIdempotencyKey(testCase.idempotencyKey))

			require.Equal(t, http.StatusBadRequest, response.Code)
			assert.Equal(t, 0, useCase.calls, "the use case must not run for a bad request")

			body := decodeError(t, response.Body.Bytes())
			assert.Equal(t, "invalid_request", body.Error.Code)
			assert.Contains(t, body.Error.Message, testCase.wantMessage)
			assert.NotEmpty(t, body.Error.RequestID)
		})
	}
}

func TestCreateOrderMapsUseCaseFailuresOntoStatuses(t *testing.T) {
	tests := map[string]struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		"out of stock": {
			err:        fmt.Errorf("reserve stock: %w", application.ErrInsufficientStock),
			wantStatus: http.StatusConflict,
			wantCode:   "insufficient_stock",
		},
		"inventory unreachable": {
			err:        fmt.Errorf("reserve stock: %w", application.ErrInventoryUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "inventory_unavailable",
		},
		"unknown product": {
			err:        fmt.Errorf("resolve product prices: %w", application.ErrProductNotFound),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "product_not_found",
		},
		"catalog unavailable": {
			err:        fmt.Errorf("resolve product prices: %w", application.ErrCatalogUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "catalog_unavailable",
		},
		"business rule rejection": {
			err:        fmt.Errorf("build order: %w", domain.ErrInvalidOrder),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation_failed",
		},
		"unexpected failure": {
			err:        errors.New("connection reset by peer"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			response := postOrder(
				t,
				&fakeUseCase{err: testCase.err},
				validBody,
				withIdempotencyKey("demo-1"),
			)

			require.Equal(t, testCase.wantStatus, response.Code)

			body := decodeError(t, response.Body.Bytes())
			assert.Equal(t, testCase.wantCode, body.Error.Code)
			assert.NotEmpty(t, body.Error.RequestID)
			assert.NotContains(
				t,
				body.Error.Message,
				"connection reset",
				"internal error text must never reach the client",
			)
		})
	}
}

func TestRouterEchoesAndSanitisesTheRequestID(t *testing.T) {
	useCase := &fakeUseCase{
		result: application.CreateOrderResult{Order: testOrder(t, true), Created: true},
	}

	accepted := postOrder(t, useCase, validBody,
		withIdempotencyKey("demo-1"), withRequestID("trace-abc_123"))
	assert.Equal(t, "trace-abc_123", accepted.Header().Get("X-Request-ID"))

	// A caller-supplied id ends up in every log line for the request, so an
	// over-long or non-alphanumeric one is replaced rather than echoed.
	rejected := postOrder(t, useCase, validBody,
		withIdempotencyKey("demo-1"), withRequestID("bad id\nwith newline"))
	echoed := rejected.Header().Get("X-Request-ID")
	assert.NotEqual(t, "bad id\nwith newline", echoed)
	assert.NotEmpty(t, echoed)
}

func TestHealthzReportsOKWithoutTouchingDependencies(t *testing.T) {
	router := httpgin.NewRouter(httpgin.RouterConfig{
		Logger:      zerolog.New(io.Discard),
		CreateOrder: &fakeUseCase{err: errors.New("everything is broken")},
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, response.Code)
}

func TestUnknownRouteAndMethodUseTheErrorEnvelope(t *testing.T) {
	router := httpgin.NewRouter(httpgin.RouterConfig{
		Logger:      zerolog.New(io.Discard),
		CreateOrder: &fakeUseCase{},
	})

	notFound := httptest.NewRecorder()
	router.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/nope", nil))
	assert.Equal(t, http.StatusNotFound, notFound.Code)
	assert.Equal(t, "invalid_request", decodeError(t, notFound.Body.Bytes()).Error.Code)

	wrongMethod := httptest.NewRecorder()
	router.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodGet, "/orders", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, wrongMethod.Code)
}

// --- helpers -------------------------------------------------------------

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func decodeError(t *testing.T, payload []byte) errorEnvelope {
	t.Helper()

	var envelope errorEnvelope
	require.NoError(t, json.Unmarshal(payload, &envelope), "body: %s", payload)

	return envelope
}

type requestOption func(*http.Request)

func withIdempotencyKey(key string) requestOption {
	return func(request *http.Request) {
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
	}
}

func withRequestID(requestID string) requestOption {
	return func(request *http.Request) {
		request.Header.Set("X-Request-ID", requestID)
	}
}

func postOrder(
	t *testing.T,
	useCase httpgin.CreateOrderUseCase,
	body string,
	options ...requestOption,
) *httptest.ResponseRecorder {
	t.Helper()

	router := httpgin.NewRouter(httpgin.RouterConfig{
		Logger:      zerolog.New(io.Discard),
		CreateOrder: useCase,
	})

	request := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	for _, option := range options {
		option(request)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	return response
}

func testOrder(t *testing.T, accepted bool) *domain.Order {
	t.Helper()

	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)

	firstItem, err := domain.NewOrderItem(
		"SKU-A",
		2,
		domain.Money{AmountInCents: 1250, Currency: "EUR"},
	)
	require.NoError(t, err)
	secondItem, err := domain.NewOrderItem(
		"SKU-B",
		1,
		domain.Money{AmountInCents: 500, Currency: "EUR"},
	)
	require.NoError(t, err)

	order, err := domain.NewOrder(
		"018f0f38-5a52-7a01-8000-000000000010",
		"018f0f38-5a52-7a01-8000-000000000020",
		"customer@example.com",
		"demo-1",
		[]domain.OrderItem{firstItem, secondItem},
		now,
	)
	require.NoError(t, err)

	if accepted {
		require.NoError(t, order.Accept(now))
	}

	return order
}

type fakeUseCase struct {
	result  application.CreateOrderResult
	err     error
	command application.CreateOrderCommand
	calls   int
}

var _ httpgin.CreateOrderUseCase = (*fakeUseCase)(nil)

func (useCase *fakeUseCase) Execute(
	_ context.Context,
	command application.CreateOrderCommand,
) (application.CreateOrderResult, error) {
	useCase.calls++
	useCase.command = command

	return useCase.result, useCase.err
}
