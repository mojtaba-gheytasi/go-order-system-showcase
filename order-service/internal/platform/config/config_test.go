package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mojtaba-gheytasi/go-order-system-showcase/order-service/internal/platform/config"
)

const validConfigFile = `ENVIRONMENT=test
DATABASE_URL=postgres://orders:secret@database:5432/orders
DB_MAX_OPEN_CONNS=20
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME=30m
HTTP_SERVER_ADDRESS=:8080
HTTP_READ_HEADER_TIMEOUT=5s
HTTP_READ_TIMEOUT=15s
HTTP_WRITE_TIMEOUT=15s
HTTP_IDLE_TIMEOUT=60s
LOG_LEVEL=info
LOG_CALLER=false
INVENTORY_GRPC_ADDRESS=inventory:50051
`

func TestLoadReadsOptionalLocalEnvironmentFile(t *testing.T) {
	directory := t.TempDir()
	writeConfigFile(t, directory, validConfigFile)

	loaded, err := config.Load(directory)
	require.NoError(t, err)

	assert.Equal(t, "test", loaded.Environment)
	assert.Equal(t, "postgres://orders:secret@database:5432/orders", loaded.DatabaseURL)
	assert.Equal(t, 20, loaded.DBMaxOpenConns)
	assert.Equal(t, 10, loaded.DBMaxIdleConns)
	assert.Equal(t, 30*time.Minute, loaded.DBConnMaxLifetime)
	assert.Equal(t, ":8080", loaded.HTTPServerAddress)
	assert.Equal(t, 5*time.Second, loaded.HTTPReadHeaderTimeout)
	assert.Equal(t, 15*time.Second, loaded.HTTPReadTimeout)
	assert.Equal(t, 15*time.Second, loaded.HTTPWriteTimeout)
	assert.Equal(t, 60*time.Second, loaded.HTTPIdleTimeout)
	assert.Equal(t, "info", loaded.LogLevel)
	require.NotNil(t, loaded.LogCaller)
	assert.False(t, *loaded.LogCaller)
	assert.Equal(t, "inventory:50051", loaded.InventoryGRPCAddress)
}

func TestLoadAllowsEnvironmentToOverrideConfigFile(t *testing.T) {
	directory := t.TempDir()
	writeConfigFile(t, directory, validConfigFile)
	t.Setenv("HTTP_SERVER_ADDRESS", ":9191")
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("LOG_CALLER", "true")

	loaded, err := config.Load(directory)
	require.NoError(t, err)

	assert.Equal(t, ":9191", loaded.HTTPServerAddress)
	assert.Equal(t, "error", loaded.LogLevel)
	require.NotNil(t, loaded.LogCaller)
	assert.True(t, *loaded.LogCaller)
}

func TestLoadRequiresConfigPath(t *testing.T) {
	_, err := config.Load("   ")

	require.Error(t, err)
	assert.ErrorContains(t, err, "config path is required")
}

func TestLoadReadsEnvironmentWithoutAFile(t *testing.T) {
	directory := t.TempDir()
	setConfigEnvironment(t, validConfigFile)

	loaded, err := config.Load(directory)
	require.NoError(t, err)

	assert.Equal(t, "test", loaded.Environment)
	assert.Equal(t, "postgres://orders:secret@database:5432/orders", loaded.DatabaseURL)
	assert.Equal(t, 20, loaded.DBMaxOpenConns)
	assert.Equal(t, ":8080", loaded.HTTPServerAddress)
	require.NotNil(t, loaded.LogCaller)
	assert.False(t, *loaded.LogCaller)
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	directory := t.TempDir()
	writeConfigFile(t, directory, `ENVIRONMENT=test
DATABASE_URL=postgres://orders
DB_MAX_OPEN_CONNS=20
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME=30m
HTTP_SERVER_ADDRESS=:8080
HTTP_READ_HEADER_TIMEOUT=5s
HTTP_READ_TIMEOUT=eventually
HTTP_WRITE_TIMEOUT=15s
HTTP_IDLE_TIMEOUT=60s
LOG_LEVEL=info
LOG_CALLER=false
INVENTORY_GRPC_ADDRESS=inventory:50051
`)

	_, err := config.Load(directory)

	require.Error(t, err)
	assert.ErrorContains(t, err, "decode configuration")
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	directory := t.TempDir()
	writeConfigFile(t, directory, `ENVIRONMENT=preview
DATABASE_URL=
DB_MAX_OPEN_CONNS=20
DB_MAX_IDLE_CONNS=10
DB_CONN_MAX_LIFETIME=30m
HTTP_SERVER_ADDRESS=:8080
HTTP_READ_HEADER_TIMEOUT=0s
HTTP_READ_TIMEOUT=15s
HTTP_WRITE_TIMEOUT=15s
HTTP_IDLE_TIMEOUT=60s
LOG_LEVEL=verbose
LOG_CALLER=false
INVENTORY_GRPC_ADDRESS=inventory:50051
`)

	_, err := config.Load(directory)

	require.Error(t, err)
	assert.ErrorContains(t, err, "validate configuration")
}

func TestLoadRejectsMissingBoolean(t *testing.T) {
	directory := t.TempDir()
	withoutLogCaller := strings.Replace(validConfigFile, "LOG_CALLER=false\n", "", 1)
	writeConfigFile(t, directory, withoutLogCaller)

	_, err := config.Load(directory)

	require.Error(t, err)
	assert.ErrorContains(t, err, "validate configuration")
}

func TestLoadIgnoresNonApplicationVariables(t *testing.T) {
	directory := t.TempDir()
	writeConfigFile(t, directory, validConfigFile+"ORDER_DB_PORT=5433\n")

	loaded, err := config.Load(directory)

	require.NoError(t, err)
	assert.Equal(t, "test", loaded.Environment)
}

func writeConfigFile(t *testing.T, directory, contents string) {
	t.Helper()

	err := os.WriteFile(
		filepath.Join(directory, ".env"),
		[]byte(contents),
		0o600,
	)
	require.NoError(t, err)
}

func setConfigEnvironment(t *testing.T, contents string) {
	t.Helper()

	for line := range strings.SplitSeq(contents, "\n") {
		if line == "" {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		require.True(t, found)
		t.Setenv(key, value)
	}
}
