package observability

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type LoggerConfig struct {
	ServiceName   string
	Environment   string
	Level         string
	IncludeCaller bool
	Output io.Writer
}

func NewLogger(config LoggerConfig) (zerolog.Logger, error) {
	if strings.TrimSpace(config.ServiceName) == "" {
		return zerolog.Nop(), fmt.Errorf("logger service name is required")
	}

	if strings.TrimSpace(config.Environment) == "" {
		return zerolog.Nop(), fmt.Errorf("logger environment is required")
	}

	levelName := strings.TrimSpace(config.Level)
	if levelName == "" {
		levelName = zerolog.InfoLevel.String()
	}

	level, err := zerolog.ParseLevel(strings.ToLower(levelName))
	if err != nil {
		return zerolog.Nop(), fmt.Errorf(
			"parse logger level %q: %w",
			config.Level,
			err,
		)
	}

	output := config.Output
	if output == nil {
		output = os.Stdout
	}

	if config.Environment == "development" {
		output = zerolog.ConsoleWriter{
			Out:        output,
			TimeFormat: time.RFC3339,
		}
	}

	// Protects the writer when multiple goroutines write concurrently
	output = zerolog.SyncWriter(output)

	loggerContext := zerolog.New(output).
		Level(level).
		With().
		Timestamp().
		Str("service", config.ServiceName).
		Str("environment", config.Environment)

	if config.IncludeCaller {
		loggerContext = loggerContext.Caller()
	}

	return loggerContext.Logger(), nil
}
