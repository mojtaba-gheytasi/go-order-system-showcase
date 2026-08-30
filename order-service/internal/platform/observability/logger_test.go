package observability_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/observability"
)

func TestNewLoggerValidatesConfig(t *testing.T) {
	tests := map[string]struct {
		config  observability.LoggerConfig
		wantErr string
	}{
		"missing service name": {
			config:  observability.LoggerConfig{Environment: "test"},
			wantErr: "service name",
		},
		"blank service name": {
			config:  observability.LoggerConfig{ServiceName: "   ", Environment: "test"},
			wantErr: "service name",
		},
		"missing environment": {
			config:  observability.LoggerConfig{ServiceName: "order"},
			wantErr: "environment",
		},
		"unknown level": {
			config: observability.LoggerConfig{
				ServiceName: "order",
				Environment: "test",
				Level:       "verbose",
			},
			wantErr: `"verbose"`,
		},
		// Also covers the TrimSpace and ToLower applied to the level.
		"padded mixed-case level": {
			config: observability.LoggerConfig{
				ServiceName: "order",
				Environment: "test",
				Level:       "  DEBUG  ",
			},
		},
	}

	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := observability.NewLogger(testCase.config)

			if testCase.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantErr)
		})
	}
}

func TestNewLoggerWritesIdentifiedJSONAtDefaultLevel(t *testing.T) {
	var buf bytes.Buffer

	logger, err := observability.NewLogger(observability.LoggerConfig{
		ServiceName: "order",
		Environment: "test",
		Output:      &buf,
	})
	require.NoError(t, err)

	logger.Debug().Msg("below the default level")
	logger.Info().Str("order_id", "ord_123").Msg("order created")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1, "an empty level must default to info, dropping debug")

	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))

	assert.Equal(t, "order created", record["message"])
	assert.Equal(t, "info", record["level"])
	assert.Equal(t, "ord_123", record["order_id"])

	// The identity fields are why ServiceName and Environment are mandatory.
	assert.Equal(t, "order", record["service"])
	assert.Equal(t, "test", record["environment"])
}
