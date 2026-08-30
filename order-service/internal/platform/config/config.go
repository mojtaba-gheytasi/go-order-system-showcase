package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

type Config struct {
	Environment           string        `mapstructure:"ENVIRONMENT" validate:"required,oneof=development test staging production"`
	DatabaseURL           string        `mapstructure:"DATABASE_URL" validate:"required"`
	HTTPServerAddress     string        `mapstructure:"HTTP_SERVER_ADDRESS" validate:"required"`
	HTTPReadHeaderTimeout time.Duration `mapstructure:"HTTP_READ_HEADER_TIMEOUT" validate:"gt=0"`
	HTTPReadTimeout       time.Duration `mapstructure:"HTTP_READ_TIMEOUT" validate:"gt=0"`
	HTTPWriteTimeout      time.Duration `mapstructure:"HTTP_WRITE_TIMEOUT" validate:"gt=0"`
	HTTPIdleTimeout       time.Duration `mapstructure:"HTTP_IDLE_TIMEOUT" validate:"gt=0"`
	LogLevel              string        `mapstructure:"LOG_LEVEL" validate:"required,oneof=trace debug info warn error fatal panic disabled"`
	LogCaller             *bool         `mapstructure:"LOG_CALLER" validate:"required"`
	InventoryGRPCAddress  string        `mapstructure:"INVENTORY_GRPC_ADDRESS" validate:"required"`
}

func Load(path string) (Config, error) {
	configPath := strings.TrimSpace(path)
	if configPath == "" {
		return Config{}, fmt.Errorf("config path is required")
	}

	reader := viper.New()
	reader.AddConfigPath(configPath)
	reader.SetConfigName("app")
	reader.SetConfigType("env")
	reader.AutomaticEnv()

	if err := reader.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	var loaded Config
	if err := reader.UnmarshalExact(&loaded); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}

	validate := validator.New(validator.WithRequiredStructEnabled())
	if err := validate.Struct(loaded); err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}

	return loaded, nil
}
