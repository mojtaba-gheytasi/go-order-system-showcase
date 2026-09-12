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

type Config struct {
	Environment           string        `mapstructure:"ENVIRONMENT" validate:"required,oneof=development test staging production"`
	DatabaseURL           string        `mapstructure:"DATABASE_URL" validate:"required"`
	DBMaxOpenConns        int           `mapstructure:"DB_MAX_OPEN_CONNS" validate:"gt=0"`
	DBMaxIdleConns        int           `mapstructure:"DB_MAX_IDLE_CONNS" validate:"gte=0"`
	DBConnMaxLifetime     time.Duration `mapstructure:"DB_CONN_MAX_LIFETIME" validate:"gt=0"`
	HTTPServerAddress     string        `mapstructure:"HTTP_SERVER_ADDRESS" validate:"required"`
	HTTPReadHeaderTimeout time.Duration `mapstructure:"HTTP_READ_HEADER_TIMEOUT" validate:"gt=0"`
	HTTPReadTimeout       time.Duration `mapstructure:"HTTP_READ_TIMEOUT" validate:"gt=0"`
	HTTPWriteTimeout      time.Duration `mapstructure:"HTTP_WRITE_TIMEOUT" validate:"gt=0"`
	HTTPIdleTimeout       time.Duration `mapstructure:"HTTP_IDLE_TIMEOUT" validate:"gt=0"`
	LogLevel              string        `mapstructure:"LOG_LEVEL" validate:"required,oneof=trace debug info warn error fatal panic disabled"`
	LogCaller             *bool         `mapstructure:"LOG_CALLER" validate:"required"`
	InventoryGRPCAddress  string        `mapstructure:"INVENTORY_GRPC_ADDRESS" validate:"required"`
	InventoryGRPCTimeout  time.Duration `mapstructure:"INVENTORY_GRPC_TIMEOUT" validate:"gt=0"`
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
