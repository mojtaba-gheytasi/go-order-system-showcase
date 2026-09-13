package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

const localEnvironmentFile = ".env"

// Config is deliberately shorter than the other services'. notification-service
// serves no HTTP and no gRPC, so it has no server address, no request timeouts and
// no connection-age settings. What it does have is a broker and a database.
type Config struct {
	Environment       string        `mapstructure:"ENVIRONMENT" validate:"required,oneof=development test staging production"`
	DatabaseURL       string        `mapstructure:"DATABASE_URL" validate:"required"`
	DBMaxOpenConns    int           `mapstructure:"DB_MAX_OPEN_CONNS" validate:"gt=0"`
	DBMaxIdleConns    int           `mapstructure:"DB_MAX_IDLE_CONNS" validate:"gte=0"`
	DBConnMaxLifetime time.Duration `mapstructure:"DB_CONN_MAX_LIFETIME" validate:"gt=0"`

	RabbitMQURL string `mapstructure:"RABBITMQ_URL" validate:"required"`

	// How many deliveries the broker may have outstanding with this consumer.
	// Explicit rather than left at the default of unlimited, which would let the
	// broker hand over the whole queue at once.
	RabbitMQPrefetchCount int `mapstructure:"RABBITMQ_PREFETCH_COUNT" validate:"gt=0"`

	// The gap between attempts to re-establish a dropped consumer.
	RabbitMQReconnectDelay time.Duration `mapstructure:"RABBITMQ_RECONNECT_DELAY" validate:"gt=0"`

	// Bounds one publication to the parked queue, including its confirmation.
	RabbitMQPublishTimeout time.Duration `mapstructure:"RABBITMQ_PUBLISH_TIMEOUT" validate:"gt=0"`

	// How long one delivery may take before the process gives up on it.
	//
	// ltfield=SendLease is the important part, and it is checked rather than
	// documented: if a handler may run longer than its claim on the effect, then
	// on any slow provider call another worker reclaims the effect and sends a
	// second email while the first is still in flight. The fencing token stops
	// the late worker corrupting the record, but it cannot un-send that email.
	HandleTimeout time.Duration `mapstructure:"HANDLE_TIMEOUT" validate:"gt=0,ltfield=SendLease"`

	// How long a claim on one notification effect is held. A worker that dies
	// mid-send leaves its claim behind; the lease is what lets another worker
	// pick the effect up instead of it being stuck as "sending" forever.
	SendLease time.Duration `mapstructure:"SEND_LEASE" validate:"gt=0"`

	// How long draining in-flight deliveries may take at shutdown.
	ShutdownTimeout time.Duration `mapstructure:"SHUTDOWN_TIMEOUT" validate:"gt=0"`

	LogLevel  string `mapstructure:"LOG_LEVEL" validate:"required,oneof=trace debug info warn error fatal panic disabled"`
	LogCaller *bool  `mapstructure:"LOG_CALLER" validate:"required"`
}

func Load(path string) (Config, error) {
	configPath := strings.TrimSpace(path)
	if configPath == "" {
		return Config{}, fmt.Errorf("config path is required")
	}

	reader := viper.New()
	reader.SetConfigFile(filepath.Join(configPath, localEnvironmentFile))
	reader.SetConfigType("env")
	reader.AutomaticEnv()

	if err := bindEnvironment(reader); err != nil {
		return Config{}, err
	}

	if err := reader.ReadInConfig(); err != nil && errors.Is(err, fs.ErrNotExist) == false {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	var loaded Config
	if err := reader.Unmarshal(&loaded); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}

	validate := validator.New(validator.WithRequiredStructEnabled())
	if err := validate.Struct(loaded); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}

	return loaded, nil
}

func bindEnvironment(reader *viper.Viper) error {
	configType := reflect.TypeFor[Config]()
	for index := range configType.NumField() {
		key := configType.Field(index).Tag.Get("mapstructure")
		if key == "" || key == "-" {
			continue
		}

		if err := reader.BindEnv(key); err != nil {
			return fmt.Errorf("bind environment variable %s: %w", key, err)
		}
	}

	return nil
}
